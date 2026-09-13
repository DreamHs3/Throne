//go:build windows

// PC-100 service-spike tests. Everything runs in-process (net.Pipe, direct
// handler calls): no SCM, no named pipe on the real machine beyond the
// listener the spike itself creates, no network, no elevation.

package main

import (
	"ThroneCore/gen"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

// ---- wire helpers (client side of the same framing) ----

func writeRequest(t *testing.T, w io.Writer, id uint32, method string, payload []byte) {
	t.Helper()
	var head [6]byte
	binary.LittleEndian.PutUint32(head[0:], id)
	binary.LittleEndian.PutUint16(head[4:], uint16(len(method)))
	if _, err := w.Write(head[:]); err != nil {
		t.Fatalf("write methodLen: %v", err)
	}
	if _, err := w.Write([]byte(method)); err != nil {
		t.Fatalf("write method: %v", err)
	}
	var pl [4]byte
	binary.LittleEndian.PutUint32(pl[:], uint32(len(payload)))
	if _, err := w.Write(pl[:]); err != nil {
		t.Fatalf("write payloadLen: %v", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("write payload: %v", err)
		}
	}
}

func readResponse(t *testing.T, r io.Reader) (uint32, uint8, []byte) {
	t.Helper()
	var head [9]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	id := binary.LittleEndian.Uint32(head[0:4])
	status := head[4]
	dataLen := binary.LittleEndian.Uint32(head[5:9])
	data := make([]byte, dataLen)
	if dataLen > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			t.Fatalf("read response data: %v", err)
		}
	}
	return id, status, data
}

func handshakePayload(t *testing.T, version int32) []byte {
	t.Helper()
	b, err := proto.Marshal(&gen.HandshakeReq{ProtocolVersion: &version})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- run mode entry point ----

func TestRunModeFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want runMode
	}{
		{nil, runModeLegacy},
		{[]string{"service"}, runModeService},
		{[]string{"bogus"}, runModeUnknown},
		{[]string{"service", "extra"}, runModeUnknown},
		{[]string{"--service"}, runModeUnknown},
	}
	for _, c := range cases {
		if got := runModeFromArgs(c.args); got != c.want {
			t.Errorf("runModeFromArgs(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestServiceConnectionSetIsGloballyBounded(t *testing.T) {
	conns := newConnSet()
	var clients []net.Conn
	defer func() {
		conns.closeAll()
		for _, client := range clients {
			_ = client.Close()
		}
	}()

	for i := 0; i < serviceMaxConnections; i++ {
		serverConn, clientConn := net.Pipe()
		clients = append(clients, clientConn)
		if !conns.tryAdd(serverConn) {
			t.Fatalf("connection %d rejected below the global limit", i)
		}
	}
	extraServer, extraClient := net.Pipe()
	defer extraServer.Close()
	defer extraClient.Close()
	if conns.tryAdd(extraServer) {
		t.Fatal("connection above the global limit must be rejected")
	}
}

func TestServiceFrameLimitRejectsBeforePayloadRead(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := readServiceRequest(context.Background(), serverConn, false)
		errCh <- err
	}()

	var header [6]byte
	binary.LittleEndian.PutUint16(header[4:], uint16(len("Start")))
	if _, err := clientConn.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write([]byte("Start")); err != nil {
		t.Fatal(err)
	}
	var payloadLength [4]byte
	binary.LittleEndian.PutUint32(payloadLength[:], serviceMaxPayloadLen+1)
	if _, err := clientConn.Write(payloadLength[:]); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "payload too large") {
		t.Fatalf("oversized frame must be rejected before its body is read, got %v", err)
	}
}

func TestServicePartialFrameGetsDeadline(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	previous := serviceReadTimeout
	serviceReadTimeout = 20 * time.Millisecond
	defer func() { serviceReadTimeout = previous }()

	errCh := make(chan error, 1)
	go func() {
		_, err := readServiceRequest(context.Background(), serverConn, true)
		errCh <- err
	}()
	if _, err := clientConn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("a one-byte frame prefix must time out")
		}
	case <-time.After(time.Second):
		t.Fatal("a one-byte frame prefix stalled without a deadline")
	}
}

// The legacy child mode decision must stay the default when no arguments are
// given, so the GUI launch path is unaffected by the service spike.
func TestLegacyModeIsDefaultAndDispatchUnchanged(t *testing.T) {
	if runModeFromArgs(nil) != runModeLegacy {
		t.Fatal("no arguments must select the legacy GUI-child mode")
	}
	// Legacy wire behavior: dispatch() serves methods directly, without any
	// handshake. CheckConfig with a malformed config returns a typed
	// ErrorResp instead of a handshake requirement.
	req := &gen.LoadConfigReq{CoreConfig: proto.String("{ definitely not json }")}
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := dispatch("CheckConfig", payload)
	if err != nil {
		t.Fatalf("CheckConfig dispatch error: %v", err)
	}
	resp := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetError() == "" {
		t.Fatal("CheckConfig must report an error for a malformed config")
	}
}

// ---- handshake and serve loop (net.Pipe: parent is `go test`, not Throne.exe) ----

func serveOverPipe(t *testing.T) (client net.Conn, done <-chan struct{}) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	finished := make(chan struct{})
	go func() {
		serveServiceConn(serverConn)
		close(finished)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("serveServiceConn did not return after client disconnect")
		}
	})
	return clientConn, finished
}

func doHandshake(t *testing.T, conn net.Conn, version int32) (uint8, []byte) {
	t.Helper()
	writeRequest(t, conn, 1, "Hello", handshakePayload(t, version))
	id, status, data := readResponse(t, conn)
	if id != 1 {
		t.Fatalf("handshake response id = %d, want 1", id)
	}
	return status, data
}

func TestServiceHandshakeVersionMismatchTypedError(t *testing.T) {
	conn, finished := serveOverPipe(t)
	status, data := doHandshake(t, conn, serviceProtocolVersion+99)
	if status != 1 {
		t.Fatalf("version mismatch must yield status=1, got %d", status)
	}
	if !strings.HasPrefix(string(data), errProtocolVersion) {
		t.Fatalf("typed error prefix missing: %q", string(data))
	}
	// Incompatible clients are disconnected after the typed error.
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server must close the connection after a version mismatch")
	}
}

func TestServiceFirstRequestMustBeHandshake(t *testing.T) {
	conn, finished := serveOverPipe(t)
	writeRequest(t, conn, 7, "Health", nil)
	_, status, data := readResponse(t, conn)
	if status != 1 || !strings.HasPrefix(string(data), errNoHandshake) {
		t.Fatalf("want %s typed error, got status=%d data=%q", errNoHandshake, status, string(data))
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server must close the connection without a handshake")
	}
}

// A service keeps running with no GUI attached: a disconnect is a normal
// event (the legacy runDispatch would log.Fatal the whole core here), and a
// subsequent client can connect and work again.
func TestServiceSurvivesClientDisconnectWithoutGUI(t *testing.T) {
	connA, _ := serveOverPipe(t)
	if status, _ := doHandshake(t, connA, serviceProtocolVersion); status != 0 {
		t.Fatalf("first handshake failed: status=%d", status)
	}
	_ = connA.Close()

	connB, _ := serveOverPipe(t)
	if status, _ := doHandshake(t, connB, serviceProtocolVersion); status != 0 {
		t.Fatalf("handshake after a disconnect failed: status=%d", status)
	}
	// Health works after reconnection; no runtime was started by anyone.
	writeRequest(t, connB, 2, "Health", nil)
	_, status, data := readResponse(t, connB)
	if status != 0 {
		t.Fatalf("Health failed: status=%d data=%q", status, string(data))
	}
	health := &gen.HealthResp{}
	if err := proto.Unmarshal(data, health); err != nil {
		t.Fatal(err)
	}
	if health.GetRuntimeRunning() {
		t.Fatal("no runtime must be running in a fresh service")
	}
	if health.GetProtocolVersion() != serviceProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", health.GetProtocolVersion(), serviceProtocolVersion)
	}
}

// ---- runtime lifecycle through the existing handlers ----

func dispatchReq(t *testing.T, method string, msg proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := dispatch(method, payload)
	if err != nil {
		t.Fatalf("dispatch(%s): %v", method, err)
	}
	return data
}

func mustErrorResp(t *testing.T, data []byte) *gen.ErrorResp {
	t.Helper()
	resp := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStopIdempotentWithoutRuntime(t *testing.T) {
	for i := 0; i < 2; i++ {
		resp := mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))
		if resp.GetError() != "" {
			t.Fatalf("Stop #%d without a runtime must be a clean no-op, got: %s", i+1, resp.GetError())
		}
	}
}

// Starting a runtime, stopping it, and stopping again: the second Stop must
// stay a clean no-op and a second Start must be rejected while one runs.
// The config below opens no inbounds/TUN/DNS — nothing touches the network.
func TestStartStopLifecycleIdempotent(t *testing.T) {
	cfg := `{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`
	startReq := &gen.LoadConfigReq{CoreConfig: proto.String(cfg), NeedExtraProcess: proto.Bool(false), NeedXray: proto.Bool(false)}
	startResp := mustErrorResp(t, dispatchReq(t, "Start", startReq))
	started := startResp.GetError() == ""
	if started {
		defer func() {
			_ = mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))
			_ = mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))
			if currentBox() != nil {
				t.Error("runtime must be gone after Stop")
			}
		}()

		dupResp := mustErrorResp(t, dispatchReq(t, "Start", startReq))
		if dupResp.GetError() == "" {
			t.Fatal("a second Start while running must be rejected")
		}
	}
	// Either way the runtime is not left behind by a failed start.
	if currentBox() != nil && !started {
		t.Fatal("a failed Start must not leave a runtime behind")
	}
}

func TestStartInvalidConfigFailsClean(t *testing.T) {
	resp := mustErrorResp(t, dispatchReq(t, "Start", &gen.LoadConfigReq{CoreConfig: proto.String("{ broken"), NeedExtraProcess: proto.Bool(false), NeedXray: proto.Bool(false)}))
	if resp.GetError() == "" {
		t.Fatal("an invalid config must fail the start")
	}
	if currentBox() != nil {
		t.Fatal("a failed start must not leave a runtime behind")
	}
}

// ---- service method allowlist (remediation of the PC-100 review P0) ----
//
// All assertions below go through serveServiceConn with real wire framing —
// after a completed Hello handshake, exactly as a service client would —
// never through a direct dispatch() call.

func wireCall(t *testing.T, conn net.Conn, id uint32, method string, payload []byte) (uint8, []byte) {
	t.Helper()
	writeRequest(t, conn, id, method, payload)
	gotID, status, data := readResponse(t, conn)
	if gotID != id {
		t.Fatalf("%s: response id = %d, want %d", method, gotID, id)
	}
	return status, data
}

func mustHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	if status, data := doHandshake(t, conn, serviceProtocolVersion); status != 0 {
		t.Fatalf("handshake failed: status=%d data=%q", status, string(data))
	}
}

// Privileged utility RPC that the legacy table carries must be unreachable
// from the service wire path and must fail with the stable typed error, and
// the connection must stay usable afterwards.
func TestServiceWirePathRejectsPrivilegedMethods(t *testing.T) {
	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	for _, method := range []string{"SetSystemDNS", "InstallDashboard", "CloseConnections", "QueryStats", "GenWgKeyPair", "DefinitelyNotAMethod"} {
		status, data := wireCall(t, conn, 10, method, nil)
		if status != 1 {
			t.Fatalf("%s: want rejection status=1, got %d", method, status)
		}
		if !strings.HasPrefix(string(data), errMethodNotAllowed) {
			t.Fatalf("%s: want %s typed error, got %q", method, errMethodNotAllowed, string(data))
		}
	}

	// The rejection is stable and non-destructive: the same connection still
	// serves allowlisted methods afterwards.
	status, _ := wireCall(t, conn, 11, "Health", nil)
	if status != 0 {
		t.Fatalf("Health after rejections must succeed, got status=%d", status)
	}
	if currentBox() != nil {
		t.Fatal("rejections must not have started a runtime")
	}
}

// The service-mode Start must refuse the whole extra-process execution
// surface (arbitrary executable path, args, config file) with the typed
// invalid-request error, before anything is parsed or spawned.
func TestServiceWireStartRejectsExtraProcess(t *testing.T) {
	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	cases := []struct {
		name string
		req  *gen.LoadConfigReq
	}{
		{
			name: "need_extra_process with arbitrary exe",
			req: &gen.LoadConfigReq{
				NeedExtraProcess: proto.Bool(true),
				ExtraProcessPath: proto.String(`C:\Windows\System32\cmd.exe`),
				ExtraProcessArgs: proto.String("/c del C:\\Users\\public\\x.txt"),
				ExtraProcessConf: proto.String(`C:\temp\extra.conf`),
			},
		},
		{
			name: "path only, flag omitted",
			req:  &gen.LoadConfigReq{ExtraProcessPath: proto.String(`C:\Windows\System32\cmd.exe`)},
		},
		{
			name: "args only",
			req:  &gen.LoadConfigReq{ExtraProcessArgs: proto.String("anything")},
		},
		{
			name: "conf only",
			req:  &gen.LoadConfigReq{ExtraProcessConf: proto.String(`C:\temp\extra.conf`)},
		},
	}
	for i, c := range cases {
		status, data := wireCall(t, conn, uint32(20+i), "Start", mustMarshal(t, c.req))
		if status != 1 {
			t.Fatalf("%s: want rejection status=1, got %d", c.name, status)
		}
		if !strings.HasPrefix(string(data), errInvalidRequest) || !strings.Contains(string(data), "not permitted in service mode") {
			t.Fatalf("%s: want %s typed error, got %q", c.name, errInvalidRequest, string(data))
		}
		if currentBox() != nil {
			t.Fatalf("%s: rejection must not start a runtime", c.name)
		}
	}

	// Stop still works on the same connection; nothing was spawned or torn down.
	status, data := wireCall(t, conn, 30, "Stop", mustMarshal(t, &gen.EmptyReq{}))
	if status != 0 {
		t.Fatalf("Stop after rejections must be a clean no-op, got status=%d data=%q", status, string(data))
	}
}

// Allowed methods over the wire: Health, CheckConfig and the full safe
// Start → Health(running) → Stop → Stop → Health(not running) lifecycle.
// The config opens no inbounds/outbounds/TUN/DNS — nothing touches the
// network or the filesystem beyond the core's own in-memory state.
func TestServiceWireAllowedMethodsAndLifecycle(t *testing.T) {
	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	// Round 3: the policy fails closed on documents the strict parser cannot
	// read, so an unparseable config is now a typed policy rejection on the
	// wire instead of an in-band config error from the runtime parser.
	status, data := wireCall(t, conn, 40, "CheckConfig", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String("{ definitely not json }"),
	}))
	if status != 1 || !strings.HasPrefix(string(data), errConfigPolicy) {
		t.Fatalf("CheckConfig must fail closed with %s on an unparseable document, got status=%d data=%q", errConfigPolicy, status, string(data))
	}

	// Strict JSON that passes the policy but fails runtime validation still
	// reports INSIDE ErrorResp (status=0): the typed policy rejection is
	// reserved for policy violations, not runtime schema errors.
	status, data = wireCall(t, conn, 46, "CheckConfig", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(`{"inbounds":[{"type":"no-such-inbound-type"}],"outbounds":[]}`),
	}))
	if status != 0 {
		t.Fatalf("CheckConfig must stay reachable over the service wire path, got status=%d data=%q", status, string(data))
	}
	checkResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, checkResp); err != nil {
		t.Fatal(err)
	}
	if checkResp.GetError() == "" {
		t.Fatal("CheckConfig must report a runtime schema error inside ErrorResp")
	}

	cfg := `{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`
	status, data = wireCall(t, conn, 41, "Start", mustMarshal(t, &gen.LoadConfigReq{CoreConfig: proto.String(cfg)}))
	if status != 0 {
		t.Fatalf("safe Start must be accepted, got status=%d data=%q", status, string(data))
	}
	startResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, startResp); err != nil {
		t.Fatal(err)
	}
	if startResp.GetError() != "" {
		t.Fatalf("safe Start failed: %s", startResp.GetError())
	}

	status, data = wireCall(t, conn, 42, "Health", nil)
	if status != 0 {
		t.Fatalf("Health while running failed: status=%d", status)
	}
	health := &gen.HealthResp{}
	if err := proto.Unmarshal(data, health); err != nil {
		t.Fatal(err)
	}
	if !health.GetRuntimeRunning() {
		t.Fatal("Health must report the running runtime")
	}

	for i := uint32(43); i <= 44; i++ {
		status, data = wireCall(t, conn, i, "Stop", mustMarshal(t, &gen.EmptyReq{}))
		if status != 0 {
			t.Fatalf("Stop #%d failed: status=%d data=%q", i-42, status, string(data))
		}
	}
	status, data = wireCall(t, conn, 45, "Health", nil)
	if status != 0 {
		t.Fatalf("Health after stop failed: status=%d", status)
	}
	if err := proto.Unmarshal(data, health); err != nil {
		t.Fatal(err)
	}
	if health.GetRuntimeRunning() {
		t.Fatal("runtime must be gone after Stop")
	}
}

// Upstream server.go dereferences proto2 optional pointers directly
// (server.go:399/434/474/579), so a request that omits fields would crash a
// handler. The service path normalizes absent fields instead — an omitted
// core_config must yield a clean typed error, never a panic, and the
// connection must stay usable.
func TestServiceWireOmittedFieldsAreNormalized(t *testing.T) {
	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	// CheckConfig with no fields at all: upstream reports config errors
	// INSIDE ErrorResp (status=0); the contract here is "clean error, no panic".
	status, data := wireCall(t, conn, 50, "CheckConfig", nil)
	if status != 0 {
		t.Fatalf("CheckConfig with an empty request must answer with a response, got status=%d data=%q", status, string(data))
	}
	checkResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, checkResp); err != nil {
		t.Fatal(err)
	}
	if checkResp.GetError() == "" || strings.Contains(checkResp.GetError(), "core panic") {
		t.Fatalf("CheckConfig must return a clean config error, got %q", checkResp.GetError())
	}

	// Start with no fields at all: same response contract, no runtime left.
	status, data = wireCall(t, conn, 51, "Start", nil)
	if status != 0 {
		t.Fatalf("Start with an empty request must answer with a response, got status=%d data=%q", status, string(data))
	}
	startEmpty := &gen.ErrorResp{}
	if err := proto.Unmarshal(data, startEmpty); err != nil {
		t.Fatal(err)
	}
	if startEmpty.GetError() == "" || strings.Contains(startEmpty.GetError(), "core panic") {
		t.Fatalf("Start must return a clean config error for an empty request, got %q", startEmpty.GetError())
	}
	if currentBox() != nil {
		t.Fatal("a failed field-less Start must not leave a runtime behind")
	}

	// need_xray without xray_config is a typed invalid request, not a panic.
	status, data = wireCall(t, conn, 52, "Start", mustMarshal(t, &gen.LoadConfigReq{
		NeedXray:   proto.Bool(true),
		CoreConfig: proto.String(`{"inbounds":[],"outbounds":[]}`),
	}))
	if status != 1 || !strings.HasPrefix(string(data), errInvalidRequest) {
		t.Fatalf("want %s for need_xray without xray_config, got status=%d data=%q", errInvalidRequest, status, string(data))
	}

	// Still serving after all of the above.
	status, _ = wireCall(t, conn, 53, "Health", nil)
	if status != 0 {
		t.Fatalf("Health after normalization cases must succeed, got status=%d", status)
	}
}

// The legacy GUI-child table must be untouched: it still carries the
// privileged utility RPC, while the service allowlist exposes exactly the
// five permitted methods and nothing else.
func TestServiceAllowlistVsLegacyTable(t *testing.T) {
	for _, method := range []string{"SetSystemDNS", "InstallDashboard", "Start", "Stop", "CheckConfig"} {
		if handlers[method] == nil {
			t.Fatalf("legacy handlers map lost %q — the GUI-child path must stay unchanged", method)
		}
	}
	want := map[string]bool{"Hello": true, "Health": true, "CheckConfig": true, "Start": true, "Stop": true}
	if len(serviceMethodAllowlist) != len(want) {
		t.Fatalf("service allowlist size = %d, want %d", len(serviceMethodAllowlist), len(want))
	}
	for method := range serviceMethodAllowlist {
		if !want[method] {
			t.Fatalf("unexpected method %q in the service allowlist", method)
		}
	}
	if _, err := dispatchService("SetSystemDNS", nil); err == nil || !strings.HasPrefix(err.Error(), errMethodNotAllowed) {
		t.Fatalf("dispatchService must reject SetSystemDNS, got %v", err)
	}
}

// ---- service-mode logging hygiene ----

// The service must never dump full core configs into logs, even when the
// child-mode debug switch (THRONE_CORE_DEBUG) is set in the environment.
func TestServiceModeNeverLogsConfigSecrets(t *testing.T) {
	t.Setenv("THRONE_CORE_DEBUG", "1")
	applyServiceModeSettings()
	if debug {
		t.Fatal("service mode must not enable the debug config dump")
	}

	var buf safeLogBuffer
	oldWriter := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(log.LstdFlags)
	}()

	const marker = "SUPER-SECRET-TEST-MARKER"
	cfg := `{"log":{"level":"warn"},"inbounds":[],"outbounds":[],"secret_marker":"` + marker + `"}`
	startResp := mustErrorResp(t, dispatchReq(t, "Start", &gen.LoadConfigReq{CoreConfig: proto.String(cfg), NeedExtraProcess: proto.Bool(false), NeedXray: proto.Bool(false)}))
	started := startResp.GetError() == ""
	_ = mustErrorResp(t, dispatchReq(t, "Stop", &gen.EmptyReq{}))

	if strings.Contains(buf.String(), marker) {
		t.Fatal("the full core config leaked into service logs")
	}
	_ = started
}

type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---- SCM handler lifecycle without SCM ----

func TestServiceHandlerExecuteLifecycleWithoutSCM(t *testing.T) {
	// Keep the Execute test off the production pipe name.
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-Execute`)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)")

	requests := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)

	handler := &proxyCoreServiceHandler{}
	exited := make(chan bool, 1)
	exitCode := make(chan uint32, 1)
	go func() {
		fire, code := handler.Execute(nil, requests, statuses)
		exited <- fire
		exitCode <- code
	}()

	// Drain statuses in the background.
	var got []svc.Status
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for s := range statuses {
			got = append(got, s)
			if s.State == svc.Stopped {
				return
			}
		}
	}()

	requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	select {
	case fire := <-exited:
		if fire {
			t.Fatal("a normal SCM stop must not request service death")
		}
		if code := <-exitCode; code != 0 {
			t.Fatalf("normal stop exit code = %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after a Stop request")
	}
	<-drainDone

	var states []svc.State
	for _, s := range got {
		states = append(states, s.State)
	}
	if len(states) == 0 || states[0] != svc.StartPending {
		t.Fatalf("first status must be StartPending, got %v", states)
	}
	if states[len(states)-1] != svc.Stopped {
		t.Fatalf("last status must be Stopped, got %v", states)
	}
	if currentBox() != nil {
		t.Fatal("service shutdown must leave no runtime running")
	}
}
