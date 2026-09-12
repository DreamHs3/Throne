package main

// PC-100 config policy (ADR-002 addendum): Start and CheckConfig receive JSON
// from the client and the privileged runtime would act on it as SYSTEM. The
// policy closes the filesystem surface:
//
//   - normalized (never client-controlled): experimental.cache_file.path is
//     rewritten to <THRONE_SERVICE_DATA_DIR>/cache.db, and the clash_api
//     external_ui* keys are removed — Throne always emits these with
//     working-directory-relative values, which the service must not resolve
//     (the service working directory is System32, not the install directory).
//   - rejected (typed ERR_CONFIG_POLICY): every other path-bearing field —
//     log sinks, TLS/OpenVPN/OpenConnect certificate & key paths (including
//     client/mTLS, CA, MCA, CRL, static keys, wrapper scripts), SSH
//     private_key_path, local/remote rule-set paths, tailscale/ACME/tor
//     directories & executables, Xray log access/error files, Xray
//     certificateFile/keyFile — plus unix:// and named-pipe/device string
//     literals anywhere in the document.
//
// Deny-by-default on known path fields makes traversal (..\), absolute paths,
// UNC and symlink/junction tricks moot: the field is never evaluated by the
// runtime, whatever its value looks like. The typed-parameter contract where
// the service builds the config itself remains the PC-110+ end state.

import (
	"encoding/json"
	"fmt"
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

// configPolicyDenyKeys rejects non-empty string values under these keys at
// any depth of the document. The list was built by enumerating every
// filesystem-bearing JSON key in the pinned sing-box `option` package (and
// the Xray certificate block); route matchers (`process_path`,
// `process_path_regex`) and the DERP `home` HTTP route are deliberately NOT
// listed — they match or route, the core never opens those values as files.
var configPolicyDenyKeys = map[string]bool{
	"output":                      true, // sing-box log.output (and any unknown sink)
	"path":                        true, // local/predefined rule-sets, hosts file, misc
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
// regardless of the key they appear under.
var configPolicyValuePrefixes = []string{
	"unix://",  // unix socket listens (v2ray api, sing-box inbounds)
	`\\.\pipe`, // Windows named pipes
	`\\.\`,     // Windows device paths
	`\\?\`,     // extended-length device paths
}

// applyServiceConfigPolicy validates a sing-box JSON document and rewrites
// the client-controlled working-directory-relative fields to service-owned
// absolute values. The returned string is the document the runtime sees.
func applyServiceConfigPolicy(coreConfigJSON string, serviceDataDir string) (string, error) {
	trimmed := strings.TrimSpace(coreConfigJSON)
	if trimmed == "" {
		return coreConfigJSON, nil
	}
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		// Not our concern: the runtime's own parser reports malformed JSON.
		return coreConfigJSON, nil
	}

	if err := normalizeServiceCoreConfig(root, serviceDataDir); err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	if err := scanConfigPolicy(root, "$", ""); err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}

	normalized, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	return string(normalized), nil
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
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil // the runtime's own parser reports malformed JSON
	}
	if err := scanConfigPolicy(root, "$", ""); err != nil {
		return fmt.Errorf("%s: %v", errConfigPolicy, err)
	}
	return nil
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
func normalizeServiceCoreConfig(root map[string]interface{}, serviceDataDir string) error {
	experimental, ok := root["experimental"].(map[string]interface{})
	if !ok {
		return nil
	}
	if cacheFile, ok := experimental["cache_file"].(map[string]interface{}); ok {
		if enabled, _ := cacheFile["enabled"].(bool); enabled {
			if serviceDataDir == "" {
				return fmt.Errorf("cache_file is enabled but the service data directory is not configured (THRONE_SERVICE_DATA_DIR)")
			}
			cacheFile["path"] = filepath.Join(serviceDataDir, "cache.db")
		} else {
			delete(cacheFile, "path")
		}
	}
	if clashAPI, ok := experimental["clash_api"].(map[string]interface{}); ok {
		delete(clashAPI, "external_ui")
		delete(clashAPI, "external_ui_download_url")
		delete(clashAPI, "external_ui_download_detour")
		if len(clashAPI) == 0 {
			delete(experimental, "clash_api")
		}
	}
	if len(experimental) == 0 {
		delete(root, "experimental")
	}
	return nil
}

// scanConfigPolicy walks the document and rejects every denied key and
// device/IPC string literal. The single exemption is
// experimental.cache_file.path, which the normalization step owns.
func scanConfigPolicy(node interface{}, where string, parentKey string) error {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, child := range value {
			if err := scanConfigPolicy(child, where+"."+key, key); err != nil {
				return err
			}
			if !configPolicyDenyKeys[key] {
				continue
			}
			// The normalized cache_file path is service-owned (this is the
			// child's full path: `where` still points at the parent here).
			if key == "path" && where+"."+key == "$.experimental.cache_file.path" {
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
		// Xray "log" objects: file sinks arrive via access/error ("output" is
		// blanket-denied above). Only "none"/empty is acceptable.
		if key0 := lastPathSegment(where); key0 == "log" {
			for _, sink := range []string{"access", "error"} {
				if v, ok := value[sink].(string); ok && v != "" && v != "none" {
					return fmt.Errorf("%s.%s: log file paths are not permitted in service configs", where, sink)
				}
			}
		}
	case []interface{}:
		for i, item := range value {
			if err := scanConfigPolicy(item, fmt.Sprintf("%s[%d]", where, i), parentKey); err != nil {
				return err
			}
		}
	case string:
		for _, prefix := range configPolicyValuePrefixes {
			if strings.HasPrefix(value, prefix) {
				return fmt.Errorf("%s: IPC/device paths (%q...) are not permitted in service configs", where, prefix)
			}
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
