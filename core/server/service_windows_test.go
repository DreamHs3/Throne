//go:build windows

// PC-100 service-spike and PC-110 envelope tests. Everything runs in-process
// (net.Pipe, direct handler calls): no SCM, no named pipe on the real machine
// beyond the listener the spike itself creates, no network, no elevation. The
// wire helpers speak the PC-110 envelope framing.

package main

import (
	"ThroneCore/gen"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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

// ---- wire helpers (client side of the PC-110 envelope framing) ----

// envFrame builds one request frame: [u32 frameLen][RequestEnvelope bytes].
func envFrame(t *testing.T, id uint64, version int32, op string, payload []byte) []byte {
	t.Helper()
	env := &gen.RequestEnvelope{
		ProtocolVersion: To(version),
		RequestId:       To(id),
		Operation:       To(op),
		TypedPayload:    payload,
	}
	body, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	return frame
}

func envWrite(t *testing.T, w io.Writer, frame []byte) {
	t.Helper()
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write envelope frame: %v", err)
	}
}

func envSend(t *testing.T, w io.Writer, id uint64, version int32, op string, payload []byte) {
	t.Helper()
	envWrite(t, w, envFrame(t, id, version, op, payload))
}

// envRead reads one response frame and decodes the ResponseEnvelope.
func envRead(t *testing.T, r io.Reader) *gen.ResponseEnvelope {
	t.Helper()
	var lenBytes [4]byte
	if _, err := io.ReadFull(r, lenBytes[:]); err != nil {
		t.Fatalf("read response frame length: %v", err)
	}
	body := make([]byte, binary.LittleEndian.Uint32(lenBytes[:]))
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read response frame body: %v", err)
	}
	resp := &gen.ResponseEnvelope{}
	if err := proto.Unmarshal(body, resp); err != nil {
		t.Fatalf("malformed response envelope: %v", err)
	}
	return resp
}

// envCall sends one request and reads its answer, asserting the response
// echoes the request id.
func envCall(t *testing.T, conn net.Conn, id uint64, op string, payload []byte) *gen.ResponseEnvelope {
	t.Helper()
	envSend(t, conn, id, serviceProtocolVersion, op, payload)
	resp := envRead(t, conn)
	if resp.GetRequestId() != id {
		t.Fatalf("%s: response request_id = %d, want %d", op, resp.GetRequestId(), id)
	}
	return resp
}

// envHello sends the handshake with the given envelope protocol version.
func envHello(t *testing.T, conn net.Conn, version int32) *gen.ResponseEnvelope {
	t.Helper()
	envSend(t, conn, 1, version, "Hello", mustMarshal(t, &gen.HandshakeReq{}))
	resp := envRead(t, conn)
	if resp.GetRequestId() != 1 {
		t.Fatalf("handshake response request_id = %d, want 1", resp.GetRequestId())
	}
	return resp
}

func mustHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	if resp := envHello(t, conn, serviceProtocolVersion); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("handshake failed: code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
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

// An oversized envelope must be refused BEFORE its body is read (and hence
// before any body buffer is allocated): the client sends only the length
// header and still gets the typed frame-too-large answer. The stream cannot
// be trusted afterwards (the claimed body may follow), so the connection is
// closed after the answer.
func TestServiceEnvelopeLimitRejectsBeforeAllocation(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := readServiceEnvelope(context.Background(), serverConn, false)
		errCh <- err
	}()

	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], serviceMaxEnvelopeLen+1)
	if _, err := clientConn.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		var protoErr *envelopeProtocolError
		if !errors.As(err, &protoErr) {
			t.Fatalf("oversized envelope must be a typed protocol error, got %v", err)
		}
		if protoErr.code != envelopeCodeFrameTooLarge || !protoErr.closeConn {
			t.Fatalf("want code=%d closeConn=true, got code=%d closeConn=%v", envelopeCodeFrameTooLarge, protoErr.code, protoErr.closeConn)
		}
		if !strings.HasPrefix(protoErr.msg, errFrameTooLarge) {
			t.Fatalf("typed prefix missing: %q", protoErr.msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an oversized envelope must be refused without reading its body")
	}
}

func TestServiceEnvelopePartialFrameGetsDeadline(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	previous := serviceReadTimeout
	serviceReadTimeout = 20 * time.Millisecond
	defer func() { serviceReadTimeout = previous }()

	errCh := make(chan error, 1)
	go func() {
		_, err := readServiceEnvelope(context.Background(), serverConn, true)
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
		serveServiceConnContext(context.Background(), serverConn, newServiceHandlerGate())
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

func TestServiceHandshakeVersionMismatchTypedError(t *testing.T) {
	conn, finished := serveOverPipe(t)
	resp := envHello(t, conn, serviceProtocolVersion+99)
	if resp.GetCode() != envelopeCodeIncompatibleVersion {
		t.Fatalf("version mismatch must yield code=%d, got %d", envelopeCodeIncompatibleVersion, resp.GetCode())
	}
	if !strings.HasPrefix(resp.GetMessage(), errProtocolVersion) {
		t.Fatalf("typed error prefix missing: %q", resp.GetMessage())
	}
	if resp.GetServiceProtocolVersion() != serviceProtocolVersion {
		t.Fatalf("response service_protocol_version = %d, want %d", resp.GetServiceProtocolVersion(), serviceProtocolVersion)
	}
	// Incompatible clients are disconnected after the typed error.
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server must close the connection after a version mismatch")
	}
}

// protocol_version is mandatory: an envelope that omits it is incompatible
// with every protocol version and gets the same typed refusal.
func TestServiceHandshakeMissingVersionTypedError(t *testing.T) {
	conn, finished := serveOverPipe(t)
	envWrite(t, conn, envFrame(t, 1, 0, "Hello", nil))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeIncompatibleVersion || !strings.HasPrefix(resp.GetMessage(), errProtocolVersion) {
		t.Fatalf("missing version must yield %s code=%d, got code=%d message=%q", errProtocolVersion, envelopeCodeIncompatibleVersion, resp.GetCode(), resp.GetMessage())
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server must close the connection without a protocol version")
	}
}

func TestServiceFirstRequestMustBeHandshake(t *testing.T) {
	conn, finished := serveOverPipe(t)
	envSend(t, conn, 7, serviceProtocolVersion, "Health", nil)
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errNoHandshake) {
		t.Fatalf("want %s code=%d, got code=%d message=%q", errNoHandshake, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
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
	mustHandshake(t, connA)
	_ = connA.Close()

	connB, _ := serveOverPipe(t)
	mustHandshake(t, connB)
	// Health works after reconnection; no runtime was started by anyone.
	resp := envCall(t, connB, 2, "Health", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health failed: code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	health := &gen.HealthResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), health); err != nil {
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

// Privileged utility RPC that the legacy table carries must be unreachable
// from the service wire path and must fail with the stable typed refusal, and
// the connection must stay usable afterwards.
func TestServiceWirePathRejectsPrivilegedMethods(t *testing.T) {
	conn, _ := serveOverPipe(t)
	mustHandshake(t, conn)

	for i, method := range []string{"SetSystemDNS", "InstallDashboard", "CloseConnections", "QueryStats", "GenWgKeyPair", "WarpRegister", "DefinitelyNotAMethod"} {
		resp := envCall(t, conn, uint64(10+i), method, nil)
		if resp.GetCode() != envelopeCodeInvalidRequest {
			t.Fatalf("%s: want code=%d, got %d (%q)", method, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
		}
		if !strings.HasPrefix(resp.GetMessage(), errMethodNotAllowed) {
			t.Fatalf("%s: want %s typed refusal, got %q", method, errMethodNotAllowed, resp.GetMessage())
		}
	}

	// The refusal is stable and non-destructive: the same connection still
	// serves registry operations afterwards.
	resp := envCall(t, conn, 11, "Health", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after refusals must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if currentBox() != nil {
		t.Fatal("refusals must not have started a runtime")
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
		resp := envCall(t, conn, uint64(20+i), "Start", mustMarshal(t, c.req))
		if resp.GetCode() != envelopeCodeInvalidRequest {
			t.Fatalf("%s: want code=%d, got %d (%q)", c.name, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
		}
		if !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) || !strings.Contains(resp.GetMessage(), "not permitted in service mode") {
			t.Fatalf("%s: want %s typed refusal, got %q", c.name, errInvalidRequest, resp.GetMessage())
		}
		if currentBox() != nil {
			t.Fatalf("%s: refusal must not start a runtime", c.name)
		}
	}

	// Stop still works on the same connection; nothing was spawned or torn down.
	resp := envCall(t, conn, 30, "Stop", mustMarshal(t, &gen.EmptyReq{}))
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Stop after refusals must be a clean no-op, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
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
	// read, so an unparseable config is now a typed policy refusal on the
	// wire (envelope code 3, ERR_CONFIG_POLICY in the message) instead of an
	// in-band config error from the runtime parser.
	resp := envCall(t, conn, 40, "CheckConfig", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String("{ definitely not json }"),
	}))
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errConfigPolicy) {
		t.Fatalf("CheckConfig must fail closed with %s, got code=%d message=%q", errConfigPolicy, resp.GetCode(), resp.GetMessage())
	}
	if len(resp.GetTypedPayload()) != 0 {
		t.Fatalf("a refused request carries no payload, got %d bytes", len(resp.GetTypedPayload()))
	}

	// Strict JSON that passes the policy but fails runtime validation still
	// reports INSIDE ErrorResp (code=0, typed payload): the envelope refusal
	// is reserved for refusals, not runtime schema errors.
	resp = envCall(t, conn, 46, "CheckConfig", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(`{"inbounds":[{"type":"no-such-inbound-type"}],"outbounds":[]}`),
	}))
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("CheckConfig must stay reachable over the service wire path, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	checkResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), checkResp); err != nil {
		t.Fatal(err)
	}
	if checkResp.GetError() == "" {
		t.Fatal("CheckConfig must report a runtime schema error inside ErrorResp")
	}

	cfg := `{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`
	resp = envCall(t, conn, 41, "Start", mustMarshal(t, &gen.LoadConfigReq{CoreConfig: proto.String(cfg)}))
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("safe Start must be accepted, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	startResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), startResp); err != nil {
		t.Fatal(err)
	}
	if startResp.GetError() != "" {
		t.Fatalf("safe Start failed: %s", startResp.GetError())
	}

	resp = envCall(t, conn, 42, "Health", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health while running failed: code=%d", resp.GetCode())
	}
	health := &gen.HealthResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), health); err != nil {
		t.Fatal(err)
	}
	if !health.GetRuntimeRunning() {
		t.Fatal("Health must report the running runtime")
	}

	for i := uint64(43); i <= 44; i++ {
		resp = envCall(t, conn, i, "Stop", mustMarshal(t, &gen.EmptyReq{}))
		if resp.GetCode() != envelopeCodeOK {
			t.Fatalf("Stop #%d failed: code=%d message=%q", i-42, resp.GetCode(), resp.GetMessage())
		}
	}
	resp = envCall(t, conn, 45, "Health", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after stop failed: code=%d", resp.GetCode())
	}
	if err := proto.Unmarshal(resp.GetTypedPayload(), health); err != nil {
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
	// INSIDE ErrorResp (code=0, typed payload); the contract here is "clean
	// error, no panic".
	resp := envCall(t, conn, 50, "CheckConfig", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("CheckConfig with an empty request must answer with a response, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	checkResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), checkResp); err != nil {
		t.Fatal(err)
	}
	if checkResp.GetError() == "" || strings.Contains(checkResp.GetError(), "core panic") {
		t.Fatalf("CheckConfig must return a clean config error, got %q", checkResp.GetError())
	}

	// Start with no fields at all: same response contract, no runtime left.
	resp = envCall(t, conn, 51, "Start", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Start with an empty request must answer with a response, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	startEmpty := &gen.ErrorResp{}
	if err := proto.Unmarshal(resp.GetTypedPayload(), startEmpty); err != nil {
		t.Fatal(err)
	}
	if startEmpty.GetError() == "" || strings.Contains(startEmpty.GetError(), "core panic") {
		t.Fatalf("Start must return a clean config error for an empty request, got %q", startEmpty.GetError())
	}
	if currentBox() != nil {
		t.Fatal("a failed field-less Start must not leave a runtime behind")
	}

	// need_xray without xray_config is a typed invalid request, not a panic.
	resp = envCall(t, conn, 52, "Start", mustMarshal(t, &gen.LoadConfigReq{
		NeedXray:   proto.Bool(true),
		CoreConfig: proto.String(`{"inbounds":[],"outbounds":[]}`),
	}))
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("want %s for need_xray without xray_config, got code=%d message=%q", errInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	// Still serving after all of the above.
	resp = envCall(t, conn, 53, "Health", nil)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after normalization cases must succeed, got code=%d", resp.GetCode())
	}
}

// The legacy GUI-child table must be untouched: it still carries the
// privileged utility RPC, while the service registry (the single PC-110
// operation table) exposes exactly the five permitted operations — and binds
// CheckConfig/Start to the Service* handlers that enforce the config policy,
// never to the raw legacy ones.
func TestServiceRegistryVsLegacyTable(t *testing.T) {
	for _, method := range []string{"SetSystemDNS", "InstallDashboard", "Start", "Stop", "CheckConfig"} {
		if handlers[method] == nil {
			t.Fatalf("legacy handlers map lost %q — the GUI-child path must stay unchanged", method)
		}
	}
	want := []string{"Hello", "Health", "CheckConfig", "Start", "Stop"}
	if len(serviceOperations) != len(want) {
		t.Fatalf("service registry size = %d, want %d", len(serviceOperations), len(want))
	}
	for _, op := range want {
		if _, ok := serviceOperations[op]; !ok {
			t.Fatalf("service registry lost %q", op)
		}
	}
	if _, ok := serviceOperations["SetSystemDNS"]; ok {
		t.Fatal("the service registry must not carry privileged legacy RPC")
	}

	// The registry binds the SERVICE handlers: a hostile config through the
	// registry's CheckConfig hits the config policy (ERR_CONFIG_POLICY),
	// while the legacy dispatch table's CheckConfig has no policy and accepts
	// the very same document (log.output is a legitimate sing-box field; the
	// GUI-child contract never had a policy). Two tables, two behaviors — the
	// service wire path only ever consults the registry.
	hostile := &gen.LoadConfigReq{CoreConfig: proto.String(`{"log":{"output":"C:\\evil.log"}}`)}
	payload := mustMarshal(t, hostile)
	_, registryErr := serviceOperations["CheckConfig"].call(context.Background(), func() proto.Message {
		msg, err := serviceOperations["CheckConfig"].decode(payload)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}())
	if registryErr == nil || !strings.HasPrefix(registryErr.Error(), errConfigPolicy) {
		t.Fatalf("registry CheckConfig must enforce the config policy, got %v", registryErr)
	}
	legacyData, legacyErr := dispatch("CheckConfig", payload)
	if legacyErr != nil {
		t.Fatalf("legacy CheckConfig dispatch: %v", legacyErr)
	}
	legacyResp := &gen.ErrorResp{}
	if err := proto.Unmarshal(legacyData, legacyResp); err != nil {
		t.Fatal(err)
	}
	if legacyResp.GetError() != "" {
		t.Fatalf("legacy CheckConfig must stay policy-free (GUI-child contract) and accept the same document, got %q", legacyResp.GetError())
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
	resetServiceRuntimeStoppingForTest(t)

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
