//go:build windows

// PC-100 service-spike tests. Everything runs in-process (net.Pipe, direct
// handler calls): no SCM, no named pipe on the real machine beyond the
// listener the spike itself creates, no network, no elevation.

package main

import (
	"ThroneCore/gen"
	"bytes"
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
