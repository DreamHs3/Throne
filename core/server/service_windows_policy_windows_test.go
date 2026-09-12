//go:build windows

// PC-100 config-policy and identity-boundary tests (remediation round 2).
//
// The filesystem policy (service_config_policy.go) is exercised directly and
// over the real service wire framing (after Hello), the SDDL override guard
// and the ADR-001 client-identity policy are exercised as pure functions, and
// one test drives a REAL named pipe (winio listen + dial in this process) to
// prove the client token identity path end to end. No SCM, no service
// installation, no network; the only named pipe created is the test pipe.

package main

import (
	"ThroneCore/gen"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/protobuf/proto"
)

// ---- config filesystem policy: rejections ----

// The matrix covers every deny key in configPolicyDenyKeys with a realistic
// document shape, plus the IPC/device value prefixes. The scan is
// shape-based, not schema-based: the runtime's own parser reports schema
// errors later, the policy must already have rejected the path.
func TestServiceConfigPolicyRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"log.output absolute", `{"log":{"output":"C:\\Windows\\Temp\\core.log"}}`},
		{"log.output traversal", `{"log":{"output":"..\\..\\evil.log"}}`},
		{"log.output relative", `{"log":{"output":"core.log"}}`},
		{"tls certificate_path", `{"outbounds":[{"type":"vless","tls":{"enabled":true,"certificate_path":"C:\\cert.pem"}}]}`},
		{"tls key_path", `{"outbounds":[{"type":"vless","tls":{"enabled":true,"key_path":"C:\\key.pem"}}]}`},
		{"tls client_certificate_path", `{"outbounds":[{"type":"vless","tls":{"enabled":true,"client_certificate_path":"C:\\client.crt"}}]}`},
		{"tls client_key_path", `{"outbounds":[{"type":"vless","tls":{"enabled":true,"client_key_path":"C:\\client.key"}}]}`},
		{"tls certificate_directory_path", `{"outbounds":[{"tls":{"certificate_directory_path":"C:\\certs"}}]}`},
		{"tls ech config_path", `{"outbounds":[{"tls":{"ech":{"enabled":true,"config_path":"C:\\ech.cfg"}}}]}`},
		{"tls acme data_directory", `{"outbounds":[{"tls":{"acme":{"data_directory":"C:\\acme"}}}]}`},
		{"ssh private_key_path", `{"outbounds":[{"type":"ssh","private_key_path":"C:\\id_ed25519"}]}`},
		{"local rule-set path", `{"route":{"rule_set":[{"type":"local","tag":"ads","path":"C:\\rules\\ads.srs"}]}}`},
		{"local rule-set paths", `{"route":{"rule_set":[{"paths":["C:\\a.srs","b.srs"]}]}}`},
		{"remote rule-set initial_path", `{"route":{"rule_set":[{"type":"remote","url":"https://example.com/r.srs","initial_path":"C:\\cache\\r.srs"}]}}`},
		{"dns hosts server path", `{"dns":{"servers":[{"type":"hosts","path":["C:\\Windows\\System32\\drivers\\etc\\hosts"]}]}}`},
		{"dns dhcp lease files", `{"dns":{"servers":[{"type":"dhcp","dhcp_lease_files":["C:\\leases"]}]}}`},
		{"openvpn static_key_path", `{"outbounds":[{"type":"openvpn","static_key_path":"C:\\static.key"}]}`},
		{"openvpn crl_path", `{"outbounds":[{"type":"openvpn","crl_path":"C:\\revoke.crl"}]}`},
		{"openconnect certificate_authority_path", `{"outbounds":[{"type":"openconnect","certificate_authority_path":"C:\\ca.pem"}]}`},
		{"openconnect mca_certificate_path", `{"outbounds":[{"type":"openconnect","mca_certificate_path":"C:\\mca.pem"}]}`},
		{"openconnect mca_key_path", `{"outbounds":[{"type":"openconnect","mca_key_path":"C:\\mca.key"}]}`},
		{"openconnect secret_path", `{"outbounds":[{"type":"openconnect","secret_path":"C:\\secret"}]}`},
		{"openconnect wrapper_path", `{"outbounds":[{"type":"openconnect","csd":{"wrapper_path":"C:\\csd.sh"}}]}`},
		{"ocm credential_path", `{"inbounds":[{"type":"ocm","credential_path":"C:\\cred"}]}`},
		{"ocm usages_path", `{"inbounds":[{"type":"ocm","usages_path":"C:\\usages"}]}`},
		{"tailscale state_directory", `{"outbounds":[{"type":"tailscale","state_directory":"C:\\ts-state"}]}`},
		{"tailscale taildrop_directory", `{"outbounds":[{"type":"tailscale","taildrop_directory":"C:\\drop"}]}`},
		{"tailscale mesh_psk_file", `{"inbounds":[{"type":"derp","mesh_psk_file":"C:\\psk"}]}`},
		{"tailscale derp config_path", `{"inbounds":[{"type":"derp","config_path":"C:\\derp.json"}]}`},
		{"tor executable_path", `{"outbounds":[{"type":"tor","executable_path":"C:\\tor.exe"}]}`},
		{"tor data_directory", `{"outbounds":[{"type":"tor","data_directory":"C:\\tor-data"}]}`},
		{"hysteria2 masquerade directory", `{"inbounds":[{"type":"hysteria2","masquerade":{"type":"file","directory":"C:\\www"}}]}`},
		{"ssmapi cache_path", `{"experimental":{"ssmapi":{"cache_path":"C:\\ssm.db"}}}`},
		{"netns pid_file", `{"inbounds":[{"type":"tun","platform":{"netns":{"pid_file":"C:\\pid"}}}]}`},
		{"tun protect_path", `{"inbounds":[{"type":"tun","protect_path":"C:\\protect.sock"}]}`},
		{"external_ui outside clash_api", `{"route":{"external_ui":"C:\\ui"}}`},
		{"external_ui_download_url outside clash_api", `{"route":{"external_ui_download_url":"https://example.com/ui.zip"}}`},
		{"listen unix socket", `{"inbounds":[{"type":"mixed","listen":"unix:///tmp/proxy.sock"}]}`},
		{"listen named pipe", `{"inbounds":[{"type":"mixed","listen":"\\\\.\\pipe\\evil"}]}`},
		{"device path prefix", `{"outbounds":[{"type":"http","server":"x","device":"\\\\?\\C:\\evil"}]}`},
	}
	for _, c := range cases {
		_, err := applyServiceConfigPolicy(c.doc, "")
		if err == nil || !strings.HasPrefix(err.Error(), errConfigPolicy) {
			t.Errorf("%s: want %s rejection, got %v", c.name, errConfigPolicy, err)
		}
	}
}

// A disabled cache_file must have its client-supplied path silently dropped:
// no error, and the normalized document must not contain the path at all.
func TestServiceConfigPolicyDropsDisabledCacheFilePath(t *testing.T) {
	doc := `{"experimental":{"cache_file":{"enabled":false,"path":"C:\\evil\\cache.db"}},"inbounds":[],"outbounds":[]}`
	out, err := applyServiceConfigPolicy(doc, "")
	if err != nil {
		t.Fatalf("disabled cache_file with a client path must be normalized, not rejected: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	cacheFile := parsed["experimental"].(map[string]interface{})["cache_file"].(map[string]interface{})
	if _, present := cacheFile["path"]; present {
		t.Fatalf("disabled cache_file path must be dropped, got: %s", out)
	}
	if enabled, _ := cacheFile["enabled"].(bool); enabled {
		t.Fatal("enabled flag must survive normalization")
	}
}

// The policy passes through what it does not own: malformed JSON and empty
// documents are the runtime parser's business, and an empty log.output is a
// legitimate boundary value.
func TestServiceConfigPolicyPassthrough(t *testing.T) {
	for _, doc := range []string{"", "{ definitely not json }"} {
		out, err := applyServiceConfigPolicy(doc, "")
		if err != nil {
			t.Fatalf("applyServiceConfigPolicy(%q) must not reject: %v", doc, err)
		}
		if out != doc {
			t.Fatalf("applyServiceConfigPolicy(%q) = %q, want unchanged", doc, out)
		}
	}
	out, err := applyServiceConfigPolicy(`{"log":{"output":""}}`, "")
	if err != nil {
		t.Fatalf("empty log.output must be accepted: %v", err)
	}
	if !strings.Contains(out, `"output":""`) {
		t.Fatalf("empty log.output must survive, got %s", out)
	}
}

// ---- config filesystem policy: normalization ----

func TestServiceConfigPolicyNormalization(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("THRONE_SERVICE_DATA_DIR", dataDir)

	doc := `{"log":{"level":"warn"},` +
		`"experimental":{"cache_file":{"enabled":true,"path":"C:\\Windows\\Temp\\evil.db"},` +
		`"clash_api":{"external_controller":"127.0.0.1:9090","external_ui":"C:\\ui","external_ui_download_url":"https://example.com/ui.zip","secret":"s"}},` +
		`"inbounds":[],"outbounds":[]}`
	out, err := applyServiceConfigPolicy(doc, configServiceDataDir())
	if err != nil {
		t.Fatalf("normalization must succeed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	experimental := parsed["experimental"].(map[string]interface{})
	cacheFile := experimental["cache_file"].(map[string]interface{})
	if got, _ := cacheFile["path"].(string); got != filepath.Join(dataDir, "cache.db") {
		t.Fatalf("cache_file.path = %q, want %q", got, filepath.Join(dataDir, "cache.db"))
	}
	clashAPI, hasClashAPI := experimental["clash_api"].(map[string]interface{})
	if !hasClashAPI {
		t.Fatal("clash_api must survive when it still carries non-path fields")
	}
	for _, key := range []string{"external_ui", "external_ui_download_url", "external_ui_download_detour"} {
		if _, present := clashAPI[key]; present {
			t.Fatalf("clash_api.%s must be removed, got: %s", key, out)
		}
	}
	if clashAPI["external_controller"] != "127.0.0.1:9090" || clashAPI["secret"] != "s" {
		t.Fatalf("non-path clash_api fields must survive, got: %s", out)
	}

	// An entirely path-only clash_api disappears together with its keys.
	out, err = applyServiceConfigPolicy(`{"experimental":{"clash_api":{"external_ui":"C:\\ui"}},"inbounds":[],"outbounds":[]}`, dataDir)
	if err != nil {
		t.Fatalf("path-only clash_api must normalize: %v", err)
	}
	if strings.Contains(out, "clash_api") {
		t.Fatalf("path-only clash_api must be removed entirely, got %s", out)
	}
}

// Fail-closed: an enabled cache_file without a service data directory must
// never fall back to a working-directory-relative path (the service working
// directory is System32).
func TestServiceConfigPolicyRequiresDataDir(t *testing.T) {
	t.Setenv("THRONE_SERVICE_DATA_DIR", "")
	_, err := applyServiceConfigPolicy(`{"experimental":{"cache_file":{"enabled":true}}}`, "")
	if err == nil || !strings.HasPrefix(err.Error(), errConfigPolicy) {
		t.Fatalf("enabled cache_file without a data dir must fail with %s, got %v", errConfigPolicy, err)
	}
}

// ---- Xray document policy ----

func TestServiceXrayConfigPolicy(t *testing.T) {
	rejected := []string{
		`{"log":{"access":"C:\\xray\\access.log","loglevel":"warning"}}`,
		`{"log":{"error":"C:\\xray\\error.log"}}`,
		`{"inbounds":[{"port":443,"streamSettings":{"tlsSettings":{"certificates":[{"certificateFile":"C:\\cert.pem","keyFile":"C:\\key.pem"}]}}}]}`,
		`{"route":{"external_ui":"C:\\ui"}}`,
		`{"inbounds":[{"listen":"unix:///tmp/x.sock"}]}`,
		`{"inbounds":[{"listen":"\\\\.\\pipe\\evil"}]}`,
	}
	for _, doc := range rejected {
		if err := validateServiceXrayConfigPolicy(doc); err == nil || !strings.HasPrefix(err.Error(), errConfigPolicy) {
			t.Errorf("Xray doc must be rejected (%s), got %v", doc, err)
		}
	}
	accepted := []string{
		``,
		`{ broken`,
		`{"log":{"loglevel":"warning"}}`,
		`{"log":{"access":"none","error":"none"}}`,
		`{"log":{"access":"","error":""}}`,
		`{"inbounds":[{"port":443}],"outbounds":[{"protocol":"freedom"}]}`,
	}
	for _, doc := range accepted {
		if err := validateServiceXrayConfigPolicy(doc); err != nil {
			t.Errorf("Xray doc must be accepted (%s), got %v", doc, err)
		}
	}
}

// ---- service wire path: policy end to end ----

// The killer test for config-based traversal: over the real wire framing, a
// Start with cache_file enabled and hostile cache paths (absolute, relative
// traversal, and a real NTFS junction) is ACCEPTED, but the runtime's cache
// database lands ONLY at <THRONE_SERVICE_DATA_DIR>/cache.db — the hostile
// destinations stay untouched. Policy rejections (ERR_CONFIG_POLICY) are
// asserted through both Start and CheckConfig for wire parity.
func TestServiceWirePolicyEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("THRONE_SERVICE_DATA_DIR", dataDir)

	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	// Whatever happens below, no runtime may survive the test (Stop is
	// idempotent by upstream contract, so a double Stop is always safe).
	defer func() {
		_ = mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))
		_ = mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))
	}()

	// A junction redirecting a client-controlled cache path to a scratch
	// directory; best-effort (junction creation needs no elevation).
	junctionTarget := filepath.Join(t.TempDir(), "junction-target")
	if err := os.MkdirAll(junctionTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(t.TempDir(), "junction-link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, junctionTarget).CombinedOutput(); err != nil {
		t.Logf("junction creation unavailable (%v: %s), skipping the junction case", err, out)
		junction = ""
	}

	cycles := []struct {
		name       string
		clientPath string
	}{
		{"absolute", `C:\Windows\Temp\evil.db`},
		{"traversal", `..\..\Windows\Temp\evil.db`},
	}
	if junction != "" {
		cycles = append(cycles, struct{ name, clientPath string }{"junction", filepath.Join(junction, "evil.db")})
	}

	for i, c := range cycles {
		cfg := `{"log":{"level":"warn"},"experimental":{"cache_file":{"enabled":true,"path":` +
			quoteJSONString(t, c.clientPath) + `}},"inbounds":[],"outbounds":[]}`
		status, data := wireCall(t, conn, uint32(100+i), "Start", mustMarshal(t, &gen.LoadConfigReq{
			CoreConfig:       proto.String(cfg),
			NeedExtraProcess: proto.Bool(false),
			NeedXray:         proto.Bool(false),
		}))
		if status != 0 {
			t.Fatalf("%s: Start with a hostile cache path must be accepted (it is rewritten), got status=%d data=%q", c.name, status, string(data))
		}
		startResp := &gen.ErrorResp{}
		if err := proto.Unmarshal(data, startResp); err != nil {
			t.Fatal(err)
		}
		if startResp.GetError() != "" {
			t.Fatalf("%s: Start failed inside the runtime: %s", c.name, startResp.GetError())
		}

		waitForFile(t, filepath.Join(dataDir, "cache.db"), 10*time.Second)

		status, data = wireCall(t, conn, uint32(200+i), "Stop", mustMarshal(t, &gen.EmptyReq{}))
		if status != 0 {
			t.Fatalf("%s: Stop failed: status=%d data=%q", c.name, status, string(data))
		}
	}

	// The data directory holds the cache database and nothing else.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache.db" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("data dir must contain only cache.db, got %v", names)
	}

	// No hostile destination was ever touched.
	assertAbsent(t, `C:\Windows\Temp\evil.db`)
	if junction != "" {
		entries, err := os.ReadDir(junctionTarget)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("junction target must stay empty, got %d entries", len(entries))
		}
	}

	// Policy rejections over the wire, through BOTH Start and CheckConfig
	// (what one rejects, the other must reject — wire parity).
	policyDoc := `{"log":{"output":"C:\\Windows\\Temp\\evil.log"}}`
	for i, method := range []string{"Start", "CheckConfig"} {
		status, data := wireCall(t, conn, uint32(300+i), method, mustMarshal(t, &gen.LoadConfigReq{CoreConfig: proto.String(policyDoc)}))
		if status != 1 || !strings.HasPrefix(string(data), errConfigPolicy) {
			t.Fatalf("%s: want %s typed wire rejection, got status=%d data=%q", method, errConfigPolicy, status, string(data))
		}
	}
	if currentBox() != nil {
		t.Fatal("rejections must not have started a runtime")
	}

	// CheckConfig must accept the same normalized document Start accepts
	// (parity in the accepting direction, including the data-dir contract).
	cfg := `{"log":{"level":"warn"},"experimental":{"cache_file":{"enabled":true}},"inbounds":[],"outbounds":[]}`
	status, data := wireCall(t, conn, 310, "CheckConfig", mustMarshal(t, &gen.LoadConfigReq{CoreConfig: proto.String(cfg)}))
	if status != 0 {
		t.Fatalf("CheckConfig must accept a normalizable config, got status=%d data=%q", status, string(data))
	}
}

// quoteJSONString encodes a Go string as a JSON string literal.
func quoteJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// waitForFile polls until path exists or the timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file did not appear within %v: %s", timeout, path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertAbsent fails the test when path exists; a stat that fails for
// reasons other than non-existence (e.g. a denied directory) cannot verify
// anything and is reported but not fatal.
func assertAbsent(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return
	}
	if err == nil {
		t.Fatalf("hostile destination was created: %s", path)
	}
	t.Logf("cannot verify absence of %s: %v", path, err)
}

// ---- SDDL override guard ----

func TestServiceSDDLValidation(t *testing.T) {
	safe := []string{
		defaultServiceSDDL,
		"D:P(A;;GA;;;S-1-5-21-1004336348-117809546-682003330-1001)",
		"D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;S-1-5-21-1-2-3)",
	}
	for _, sddl := range safe {
		got, err := safeServiceSDDL(sddl)
		if err != nil {
			t.Errorf("safeServiceSDDL(%q) must be accepted, got %v", sddl, err)
		} else if got != sddl {
			t.Errorf("safeServiceSDDL(%q) = %q, want unchanged", sddl, got)
		}
	}
	unsafe := []string{
		"D:P(A;;GA;;;WD)",             // Everyone
		"D:P(A;;GA;;;AN)",             // Anonymous
		"D:P(A;;GA;;;AU)",             // Authenticated Users
		"D:P(A;;GA;;;BU)",             // Builtin Users
		"D:P(A;;GA;;;SY)(A;;GA;;;WD)", // broad trustee behind a valid prefix
		"D:(A;;GA;;;SY)",              // DACL without the protected flag
		"O:SYD:P(A;;GA;;;SY)",         // owner section first, not a bare protected DACL
		"",                            // no DACL at all
	}
	for _, sddl := range unsafe {
		if _, err := safeServiceSDDL(sddl); err == nil || !strings.HasPrefix(err.Error(), errInvalidRequest) {
			t.Errorf("safeServiceSDDL(%q) must be rejected with %s, got %v", sddl, errInvalidRequest, err)
		}
	}
}

// ---- ADR-001 client identity policy (pure function) ----

func TestServiceClientAllowedPolicy(t *testing.T) {
	owner := "S-1-5-21-1004336348-117809546-682003330-1001"
	second := "S-1-5-21-1004336348-117809546-682003330-1002"
	foreign := "S-1-5-21-999-888-777-666"

	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", owner)
	if !serviceClientAllowed(owner, nil) {
		t.Fatal("the configured owner SID must be allowed")
	}

	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", owner+", "+second)
	if !serviceClientAllowed(second, nil) {
		t.Fatal("a second configured SID (with whitespace) must be allowed")
	}

	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", owner)
	if !serviceClientAllowed(foreign, []string{builtinAdministratorsSID}) {
		t.Fatal("Administrators group membership must be allowed regardless of the owner list")
	}

	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", owner)
	if serviceClientAllowed(foreign, []string{"S-1-5-32-545"}) {
		t.Fatal("a foreign SID without Administrators must be denied")
	}

	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", "")
	if serviceClientAllowed(foreign, nil) {
		t.Fatal("with no owner list and no Administrators, everyone must be denied")
	}
	if serviceClientAllowed("", nil) {
		t.Fatal("an empty SID must never match the allowlist")
	}
	if !serviceClientAllowed("", []string{builtinAdministratorsSID}) {
		t.Fatal("Administrators membership is a group decision and must not depend on the user SID")
	}
}

// ---- real pipe: client token identity end to end ----

// currentProcessIdentity resolves this process's user and group SIDs
// independently of the service path under test.
func currentProcessIdentity(t *testing.T) (string, []string) {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatalf("OpenProcessToken: %v", err)
	}
	defer token.Close()
	buf, err := tokenInformation(token, windows.TokenUser)
	if err != nil {
		t.Fatalf("TokenUser: %v", err)
	}
	sid := (*windows.Tokenuser)(unsafe.Pointer(&buf[0])).User.Sid.String()
	var groups []string
	if gbuf, err := tokenInformation(token, windows.TokenGroups); err == nil {
		groups = tokenGroupSIDs(gbuf)
	}
	return sid, groups
}

func startRealPipeService(t *testing.T) {
	t.Helper()
	listener, err := listenServicePipe()
	if err != nil {
		t.Fatalf("listenServicePipe: %v", err)
	}
	conns := newConnSet()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveServiceListener(listener, conns, stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		_ = listener.Close()
		conns.closeAll()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("service accept loop did not stop")
		}
	})
}

func dialServicePipe(t *testing.T) net.Conn {
	t.Helper()
	timeout := 5 * time.Second
	conn, err := winio.DialPipe(servicePipeName(), &timeout)
	if err != nil {
		t.Fatalf("DialPipe(%s): %v", servicePipeName(), err)
	}
	return conn
}

// The full identity chain against a REAL pipe: the listener's DACL grants
// only this process's own SID (the PC-120 installer pattern), and the
// allowlist contains only that SID. The handshake can therefore only succeed
// if clientTokenIdentity resolved the connecting process's token correctly —
// any other identity would be denied and the connection closed unread.
// When the runner is not an administrator, a second connection with an empty
// allowlist must be denied despite the DACL grant: token identity gates what
// the DACL admits.
func TestServiceClientIdentityRealPipe(t *testing.T) {
	selfSID, selfGroups := currentProcessIdentity(t)
	isAdmin := false
	for _, group := range selfGroups {
		if group == builtinAdministratorsSID {
			isAdmin = true
			break
		}
	}

	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-Identity`)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
	startRealPipeService(t)

	conn := dialServicePipe(t)
	mustHandshake(t, conn)
	_ = conn.Close()

	// Denied direction: the DACL admits us, the identity policy must not.
	// The server closes the connection during authorization, before it ever
	// reads a request — an EOF on read is the denial signal.
	if isAdmin {
		t.Log("runner is elevated; the denial case is skipped because Administrators group membership is always admitted")
		return
	}
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", "")
	conn = dialServicePipe(t)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, _, _, err := tryReadResponse(conn); err == nil {
		t.Fatal("a DACL-admitted client with an empty allowlist must be denied by token identity")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the service neither served nor denied the client within 5s")
	}
	_ = conn.Close()
}

// tryReadResponse reads a service response without failing the test; used
// where an error (EOF after a server-side close) IS the expected outcome.
func tryReadResponse(r io.Reader) (uint32, uint8, []byte, error) {
	var head [9]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, 0, nil, err
	}
	id := binary.LittleEndian.Uint32(head[0:4])
	status := head[4]
	dataLen := binary.LittleEndian.Uint32(head[5:9])
	data := make([]byte, dataLen)
	if dataLen > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			return 0, 0, nil, err
		}
	}
	return id, status, data, nil
}
