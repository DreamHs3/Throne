package main

// PC-100 config policy (ADR-002 addendum): Start and CheckConfig receive JSON
// from the client and the privileged runtime would act on it as SYSTEM. The
// policy closes the filesystem surface:
//
//   - the document is parsed strictly with std encoding/json semantics (no
//     comments, exact numbers, no trailing data). A document the strict parser
//     cannot read is REJECTED, never passed through: the privileged runtimes
//     parse a strictly larger language (sing contextjson strips JSONC
//     comments and binds keys case-insensitively; Xray's serial decoder
//     accepts Java/Python comments), so "std-json can't read it, the runtime
//     will" was a proven unscanned-document bypass (round 3).
//   - keys are matched CASE-INSENSITIVELY against the deny list for the same
//     reason: the runtime binders accept any case spelling, so
//     {"LOG":{"OUTPUT":...}} would carry an unscanned log sink into the
//     runtime (the re-marshaled document preserves the client's spelling).
//   - normalized (never client-controlled): experimental.cache_file.path is
//     rewritten to <THRONE_SERVICE_DATA_DIR>/cache.db, and the clash_api
//     external_ui* keys are removed — key lookup case-insensitive (as above;
//     every case variant of an owned key is deleted before the service-owned
//     value is written), and Throne always emits these fields with
//     working-directory-relative values, which the service must not resolve
//     (the service working directory is System32, not the install directory).
//   - rejected (typed ERR_CONFIG_POLICY): every other path-bearing field —
//     log sinks, TLS/OpenVPN/OpenConnect certificate & key paths (including
//     client/mTLS, CA, MCA, CRL, static keys, wrapper scripts), SSH
//     private_key_path, local/remote rule-set paths, tailscale/ACME/tor
//     directories & executables, Xray log access/error files, Xray
//     certificateFile/keyFile — plus unix-socket and named-pipe/device string
//     literals anywhere in the document (prefix match case-insensitive:
//     Windows pipe/device namespaces are case-insensitive).
//
// Deny-by-default on known path fields makes traversal (..\), absolute paths,
// UNC and symlink/junction tricks moot: the field is never evaluated by the
// runtime, whatever its value looks like. The typed-parameter contract where
// the service builds the config itself remains the PC-110+ end state.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const errConfigPolicy = "ERR_CONFIG_POLICY"

// configServiceDataDir is the only directory the normalized config may point
// the runtime at (cache.db). Set by the service installer (PC-120); absent in
// the spike unless a test sets it.
func configServiceDataDir() string {
	return os.Getenv("THRONE_SERVICE_DATA_DIR")
}

// configPolicyDenyKeys rejects non-empty string values under these keys (any
// case spelling) at any depth of the document. The list was built by
// enumerating every filesystem-bearing JSON key in the pinned sing-box
// `option` package (and the Xray certificate block); route matchers
// (`process_path`, `process_path_regex`) and the DERP `home` HTTP route are
// deliberately NOT listed — they match or route, the core never opens those
// values as files.
var configPolicyDenyKeys = map[string]bool{
	"output":                      true, // sing-box log.output (and any unknown sink)
	"paths":                       true,
	"initial_path":                true, // remote rule-set initial cache file
	"cache_path":                  true, // ssmapi experimental service
	"config_path":                 true, // ECH config, tailscale DERP
	"certificate_path":            true, // sing-box TLS / openvpn / openconnect
	"key_path":                    true,
	"client_certificate_path":     true, // sing-box TLS / openvpn / openconnect mTLS
	"client_key_path":             true,
	"certificate_directory_path":  true,
	"certificate_authority_path":  true, // openconnect
	"mca_certificate_path":        true, // openconnect
	"mca_key_path":                true,
	"crl_path":                    true, // openvpn
	"static_key_path":             true, // openvpn
	"private_key_path":            true, // sing-box ssh
	"secret_path":                 true, // openconnect
	"credential_path":             true, // ocm/ccm services
	"usages_path":                 true, // ocm/ccm services
	"executable_path":             true, // tor (process execution)
	"wrapper_path":                true, // openconnect CSD/HIP/TNCC scripts
	"protect_path":                true, // sing-tun protect socket
	"data_directory":              true, // acme / origin_ca / tor
	"state_directory":             true, // tailscale
	"taildrop_directory":          true, // tailscale file drop
	"mesh_psk_file":               true, // tailscale DERP mesh
	"pid_file":                    true, // netns
	"dhcp_lease_files":            true, // route dhcp
	"directory":                   true, // hysteria2 masquerade file server
	"certificateFile":             true, // Xray tls certificates
	"keyFile":                     true,
	"external_ui":                 true, // clash api UI directory (removed by normalize)
	"external_ui_download_url":    true, // core-driven downloads
	"external_ui_download_detour": true,
}

// configPolicyValuePrefixes rejects string values that name IPC/device paths
// regardless of the key they appear under. Matching is case-insensitive
// (configPolicyValueDenied): Windows pipe and device namespaces are
// case-insensitive, and "unix:" also covers the bare gRPC socket form.
var configPolicyValuePrefixes = []string{
	"unix:",    // unix sockets (gRPC bare form; subsumes "unix://")
	`\\.\pipe`, // Windows named pipes
	`\\.\`,     // Windows device paths
	`\\?\`,     // extended-length device paths
}

// applyServiceConfigPolicy validates a sing-box JSON document and rewrites
// the client-controlled working-directory-relative fields to service-owned
// absolute values. The returned string is the document the runtime sees —
// exactly the scanned tree re-marshaled (scan-what-you-run), with numbers
// preserved lexically.
func applyServiceConfigPolicy(coreConfigJSON string, serviceDataDir string) (string, error) {
	trimmed := strings.TrimSpace(coreConfigJSON)
	if trimmed == "" {
		return coreConfigJSON, nil
	}
	root, err := parsePolicyDocument(trimmed)
	if err != nil {
		// Fail closed: a document the strict parser cannot read but a
		// runtime parser (JSONC comments) can is an unscanned bypass.
		// Genuinely malformed JSON is rejected by the runtime anyway.
		return "", fmt.Errorf("%s: not valid strict JSON: %v", errConfigPolicy, err)
	}

	if err := normalizeServiceCoreConfig(root, serviceDataDir); err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	if err := scanSingBoxFilesystemPolicy(root, "$", false, false, false); err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	if err := scanConfigPolicy(root, "$", "", true); err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}

	normalized, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	return string(normalized), nil
}

// scanSingBoxFilesystemPolicy distinguishes filesystem-bearing `path` fields
// from ordinary URL paths used by WebSocket, HTTP, HTTPUpgrade and DNS HTTPS.
func scanSingBoxFilesystemPolicy(node interface{}, where string, inRuleSet bool, inNetworkNamespaces bool, inLegacyGeo bool) error {
	switch value := node.(type) {
	case map[string]interface{}:
		objectType, _ := lookupMapString(value, "type")
		for key, child := range value {
			childWhere := where + "." + key
			childInRuleSet := inRuleSet || strings.EqualFold(key, "rule_set")
			childInNetworkNamespaces := inNetworkNamespaces || strings.EqualFold(key, "network_namespaces")
			childInLegacyGeo := inLegacyGeo || (strings.EqualFold(lastPathSegment(where), "route") &&
				(strings.EqualFold(key, "geoip") || strings.EqualFold(key, "geosite")))
			if strings.EqualFold(key, "path") && stringOrListHasNonEmpty(child) {
				filesystemPath := childInNetworkNamespaces || childInLegacyGeo ||
					(childInRuleSet && strings.EqualFold(objectType, "local")) || strings.EqualFold(objectType, "hosts")
				if filesystemPath {
					return fmt.Errorf("%s: filesystem path is not permitted in service configs", childWhere)
				}
			}
			if err := scanSingBoxFilesystemPolicy(child, childWhere, childInRuleSet, childInNetworkNamespaces, childInLegacyGeo); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, child := range value {
			if err := scanSingBoxFilesystemPolicy(child, fmt.Sprintf("%s[%d]", where, i), inRuleSet, inNetworkNamespaces, inLegacyGeo); err != nil {
				return err
			}
		}
	}
	return nil
}

func lookupMapString(value map[string]interface{}, wanted string) (string, bool) {
	for key, child := range value {
		if strings.EqualFold(key, wanted) {
			text, ok := child.(string)
			return text, ok
		}
	}
	return "", false
}

func stringOrListHasNonEmpty(value interface{}) bool {
	switch typed := value.(type) {
	case string:
		return typed != ""
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				return true
			}
		}
	}
	return false
}

// validateServiceXrayConfigPolicy applies the rejection rules to an Xray
// document (log.access/log.error files, certificateFile/keyFile, ...). No
// normalization: Throne's Xray bridge emits no path fields, so any hit is a
// client-supplied path.
func validateServiceXrayConfigPolicy(xrayConfigJSON string) error {
	trimmed := strings.TrimSpace(xrayConfigJSON)
	if trimmed == "" {
		return nil
	}
	root, err := parsePolicyDocument(trimmed)
	if err != nil {
		// Fail closed: Xray's serial decoder accepts Java/Python comments
		// the strict parser rejects; anything unscannable is rejected.
		return fmt.Errorf("%s: not valid strict JSON: %v", errConfigPolicy, err)
	}
	if err := scanXrayFilesystemPolicy(root, "$", false, false); err != nil {
		return fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	if err := scanConfigPolicy(root, "$", "", true); err != nil {
		return fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	return nil
}

// scanXrayFilesystemPolicy covers Xray-only file fields without rejecting
// unrelated generic "file" keys in sing-box documents. masterKeyLog="none"
// is Xray's documented disabled sentinel and remains a legitimate value.
func scanXrayFilesystemPolicy(node interface{}, where string, insideGeodata bool, insideMasquerade bool) error {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, child := range value {
			childWhere := where + "." + key
			childInGeodata := insideGeodata || strings.EqualFold(key, "geodata")
			childInMasquerade := insideMasquerade || strings.EqualFold(key, "masquerade")
			if where == "$" && strings.EqualFold(key, "env") {
				if env, ok := child.(map[string]interface{}); ok && len(env) > 0 {
					return fmt.Errorf("%s: Xray environment overrides are not permitted in service configs", childWhere)
				}
			}
			if strings.EqualFold(key, "masterKeyLog") {
				if path, ok := child.(string); ok && path != "" && path != "none" {
					return fmt.Errorf("%s: Xray master key log files are not permitted in service configs", childWhere)
				}
			}
			if childInGeodata && strings.EqualFold(key, "file") {
				if path, ok := child.(string); ok && path != "" {
					return fmt.Errorf("%s: Xray geodata asset files are not permitted in service configs", childWhere)
				}
			}
			if childInMasquerade && strings.EqualFold(key, "dir") {
				if path, ok := child.(string); ok && path != "" {
					return fmt.Errorf("%s: Xray masquerade directories are not permitted in service configs", childWhere)
				}
			}
			if err := scanXrayFilesystemPolicy(child, childWhere, childInGeodata, childInMasquerade); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, child := range value {
			if err := scanXrayFilesystemPolicy(child, fmt.Sprintf("%s[%d]", where, i), insideGeodata, insideMasquerade); err != nil {
				return err
			}
		}
	}
	return nil
}

// parsePolicyDocument parses a JSON document with std encoding/json
// strictness into the scanned shape: no comments, no trailing data, no
// non-object roots. Numbers stay json.Number so the re-marshal reproduces
// them exactly (a float64 round trip silently rewrote large int64 values).
func parsePolicyDocument(trimmed string) (map[string]interface{}, error) {
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	// Decoder.Decode reads only the first value; std json.Unmarshal also
	// rejects trailing data, and the runtime parsers might accept it — keep
	// the strictness.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing data after the JSON document")
	}
	return root, nil
}

// normalizeServiceCoreConfig rewrites the two fields Throne always emits with
// working-directory-relative values:
//   - experimental.cache_file.path → <serviceDataDir>/cache.db (required when
//     cache_file is enabled; otherwise the runtime would create cache.db in
//     its working directory — System32 for a Windows service). When
//     cache_file is disabled, a client-supplied path is dropped.
//   - experimental.clash_api.external_ui* keys are removed (the UI is not the
//     core's business, and the value is a directory the core would serve or
//     download into).
//
// Key lookup is case-insensitive: the runtime binder accepts EXPERIMENTAL /
// CACHE_FILE / ENABLED / PATH exactly like the lowercase spellings, so a
// case-variant object would otherwise bypass both the rewrite and the scan
// exemption. Case-variant spellings of the owned keys are deleted, never
// overwritten (an added canonical key would leave the hostile spelling in
// the re-marshaled document).
func normalizeServiceCoreConfig(root map[string]interface{}, serviceDataDir string) error {
	for key, value := range root {
		if !strings.EqualFold(key, "experimental") {
			continue
		}
		experimental, ok := value.(map[string]interface{})
		if !ok {
			// Not an object: nothing to normalize; the runtime parser
			// rejects it and the scan below still runs on the document.
			continue
		}
		if err := normalizeCacheFile(experimental, serviceDataDir); err != nil {
			return err
		}
		normalizeClashAPI(experimental)
		if len(experimental) == 0 {
			delete(root, key)
		}
	}
	return nil
}

// normalizeCacheFile handles every case-variant spelling of
// experimental.cache_file inside one experimental object.
func normalizeCacheFile(experimental map[string]interface{}, serviceDataDir string) error {
	for key, value := range experimental {
		if !strings.EqualFold(key, "cache_file") {
			continue
		}
		cacheFile, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		// Fail closed on ambiguity: if any spelling claims enabled=true, the
		// data-dir contract applies (a path we write for a runtime that ends
		// up disabled is harmless; the reverse direction is not).
		enabled := false
		for k, v := range cacheFile {
			if strings.EqualFold(k, "enabled") {
				if b, ok := v.(bool); ok && b {
					enabled = true
				}
			}
		}
		for k := range cacheFile {
			if strings.EqualFold(k, "path") {
				delete(cacheFile, k)
			}
		}
		if enabled {
			if serviceDataDir == "" {
				return fmt.Errorf("cache_file is enabled but the service data directory is not configured (THRONE_SERVICE_DATA_DIR)")
			}
			cacheFile["path"] = filepath.Join(serviceDataDir, "cache.db")
		}
	}
	return nil
}

// normalizeClashAPI handles every case-variant spelling of
// experimental.clash_api inside one experimental object.
func normalizeClashAPI(experimental map[string]interface{}) {
	for key, value := range experimental {
		if !strings.EqualFold(key, "clash_api") {
			continue
		}
		clashAPI, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		for k := range clashAPI {
			if strings.EqualFold(k, "external_ui") ||
				strings.EqualFold(k, "external_ui_download_url") ||
				strings.EqualFold(k, "external_ui_download_detour") {
				delete(clashAPI, k)
			}
		}
		if len(clashAPI) == 0 {
			delete(experimental, key)
		}
	}
}

// configPolicyKeyDenied reports whether key names (case-insensitively) a
// denied key. EqualFold, not a map lookup: the runtime's JSON binder matches
// struct fields case-insensitively, so "OUTPUT" binds to log.output exactly
// like "output" does.
func configPolicyKeyDenied(key string) bool {
	for denied := range configPolicyDenyKeys {
		if strings.EqualFold(denied, key) {
			return true
		}
	}
	return false
}

// configPolicyValueDenied reports whether value names an IPC/device path,
// case-insensitively: \\.\PIPE\evil names the same pipe as \\.\pipe\evil.
func configPolicyValueDenied(value string) bool {
	folded := strings.ToLower(value)
	for _, prefix := range configPolicyValuePrefixes {
		if strings.HasPrefix(folded, prefix) {
			return true
		}
	}
	return false
}

// scanConfigPolicy walks the document and rejects every denied key and
// device/IPC string literal. The single exemption is
// experimental.cache_file.path (any case spelling — normalization owns them
// all, and every spelling it does not own has been deleted by the time the
// scan runs).
func scanConfigPolicy(node interface{}, where string, parentKey string, allowGenericPath bool) error {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, child := range value {
			if err := scanConfigPolicy(child, where+"."+key, key, allowGenericPath); err != nil {
				return err
			}
			if !configPolicyKeyDenied(key) && (allowGenericPath || !strings.EqualFold(key, "path")) {
				continue
			}
			// The normalized cache_file path is service-owned (this is the
			// child's full path: `where` still points at the parent here).
			if strings.EqualFold(key, "path") && strings.EqualFold(where+"."+key, "$.experimental.cache_file.path") {
				continue
			}
			switch typed := child.(type) {
			case string:
				if typed != "" {
					return fmt.Errorf("%s: %q is not permitted in service configs", where+"."+key, key)
				}
			case []interface{}:
				for _, item := range typed {
					if s, ok := item.(string); ok && s != "" {
						return fmt.Errorf("%s: %q is not permitted in service configs", where+"."+key, key)
					}
				}
			}
		}
		// Xray "log" objects (any case spelling of the object and of the
		// sink keys): file sinks arrive via access/error ("output" is
		// blanket-denied above). Only "none"/empty is acceptable.
		if strings.EqualFold(lastPathSegment(where), "log") {
			for key, sink := range value {
				if !strings.EqualFold(key, "access") && !strings.EqualFold(key, "error") {
					continue
				}
				if v, ok := sink.(string); ok && v != "" && v != "none" {
					return fmt.Errorf("%s.%s: log file paths are not permitted in service configs", where, key)
				}
			}
		}
	case []interface{}:
		for i, item := range value {
			if err := scanConfigPolicy(item, fmt.Sprintf("%s[%d]", where, i), parentKey, allowGenericPath); err != nil {
				return err
			}
		}
	case string:
		if configPolicyValueDenied(value) {
			return fmt.Errorf("%s: IPC/device paths are not permitted in service configs", where)
		}
	}
	return nil
}

func lastPathSegment(where string) string {
	if idx := strings.LastIndex(where, "."); idx >= 0 {
		return where[idx+1:]
	}
	return where
}
