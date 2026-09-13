//go:build windows

// PC-110 remediation regression tests: the request deadline must act during
// the handler-slot waits, the shutdown admission barrier must make a
// post-shutdown handler dispatch (and a WaitGroup.Add racing the shutdown
// Wait) impossible, the Accept/shutdown race must not serve a connection,
// and the mandatory Hello must obey the same envelope contract as every
// other frame. Everything runs through the REAL serveServiceConnContext (or
// the real proxyCoreServiceHandler.Execute) — never through a stub of the
// serve loop.

package main

import (
	"ThroneCore/gen"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

// ---- shared observation helpers ----

// resetServiceRuntimeStoppingForTest clears the barrier state that the
// Execute stop path leaves for the process lifetime. Production exits right
// after Stopped and every Execute run resets the barrier at start, so the
// mark never needs clearing there; a test suite keeps running past a stopped
// run (and -count=N reuses the process), so every test that drives Execute
// to a Stop resets it.
func resetServiceRuntimeStoppingForTest(t *testing.T) {
	t.Cleanup(func() {
		resetServiceRuntimeBarrier()
	})
}

// serveWithGate drives the real serveServiceConnContext with a test-owned
// admission gate so the tests can observe the handler accounting (inflight)
// while the global slot and payload-budget limits are saturated directly.
func serveWithGate(t *testing.T, ctx context.Context) (client net.Conn, gate *serviceHandlerGate, finished <-chan struct{}) {
	t.Helper()
	gate = newServiceHandlerGate()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		serveServiceConnContext(ctx, serverConn, gate)
		close(done)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serveServiceConnContext did not return after client disconnect")
		}
	})
	return clientConn, gate, done
}

// waitAdmissionInflight polls until the gate holds exactly want in-flight
// handlers. Polling an observable counter is the synchronization; the sleep
// is only the poll interval.
func waitAdmissionInflight(t *testing.T, g *serviceHandlerGate, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g.inflight() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("admission gate inflight = %d, want %d", g.inflight(), want)
}

// waitGlobalSlotsIdle polls until every global handler slot has been
// released (responses can be written slightly before the releasing defers
// run, so the drain is polled, not assumed).
func waitGlobalSlotsIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(serviceHandlerSlots) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("global handler slots = %d still held, want 0", len(serviceHandlerSlots))
}

// assertPayloadBudgetIntact proves the aggregate envelope budget leaked
// nothing: the full budget must be acquirable.
func assertPayloadBudgetIntact(t *testing.T) {
	t.Helper()
	if !servicePayloadSlots.TryAcquire(servicePayloadBudget) {
		t.Fatal("aggregate payload budget leaked: the full budget is not acquirable")
	}
	servicePayloadSlots.Release(servicePayloadBudget)
}

// envHelloID is envHello with an explicit request id (the dedup tests need
// handshake ids other than 1).
func envHelloID(t *testing.T, conn net.Conn, id uint64) *gen.ResponseEnvelope {
	t.Helper()
	envSend(t, conn, id, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{}))
	resp := envRead(t, conn)
	if resp.GetRequestId() != id {
		t.Fatalf("handshake response request_id = %d, want %d", resp.GetRequestId(), id)
	}
	return resp
}

// envExpiredHello builds an already-expired Hello frame.
func envExpiredHello(t *testing.T, id uint64) *gen.RequestEnvelope {
	t.Helper()
	return &gen.RequestEnvelope{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		RequestId:       To(id),
		Operation:       To("Hello"),
		DeadlineUnixMs:  To(time.Now().Add(-time.Hour).UnixMilli()),
		TypedPayload:    mustMarshal(t, &gen.HandshakeReq{}),
	}
}

// ---- P1: the shutdown admission barrier ----

// The gate's contract, directly: admission is open until beginShutdown, and
// beginShutdown closes BOTH handler and connection admission synchronously —
// so no WaitGroup.Add can happen after the shutdown Wait has become
// possible, and the Wait observes exactly the handlers admitted before it.
// (The mutex that orders admit against beginShutdown is exercised under
// -race by the whole suite.)
func TestServiceHandlerGateAdmissionBarrier(t *testing.T) {
	g := newServiceHandlerGate()
	if !g.admitConn() {
		t.Fatal("connection admission must be open before shutdown")
	}
	if !g.admit() || !g.admit() {
		t.Fatal("handler admission must be open before shutdown")
	}
	if g.inflight() != 2 {
		t.Fatalf("inflight = %d, want 2", g.inflight())
	}

	handlersDone := g.beginShutdown()

	if g.admit() {
		t.Fatal("handler admission must be closed synchronously by beginShutdown")
	}
	if g.admitConn() {
		t.Fatal("connection admission must be closed synchronously by beginShutdown")
	}

	g.release()
	g.release()
	select {
	case <-handlersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown Wait must observe the handlers admitted before it")
	}
	if g.inflight() != 0 {
		t.Fatalf("inflight = %d, want 0 after both releases", g.inflight())
	}
}

// The Accept/shutdown race: a connection whose Accept succeeds AFTER the
// admission barrier closed is closed by the accept loop — it is never
// served, so no handshake and no handler can start past the barrier.
func TestServiceListenerAdmissionClosesWithShutdown(t *testing.T) {
	prevAuthorize := authorizeServiceClient
	authorizeServiceClient = func(net.Conn) bool { return true }
	t.Cleanup(func() { authorizeServiceClient = prevAuthorize })

	gate := newServiceHandlerGate()
	// Close admission BEFORE the loop runs: every connection the loop then
	// accepts arrives after the barrier.
	gate.beginShutdown()

	l := &stubServiceListener{accepted: make(chan net.Conn, 1), closed: make(chan struct{})}
	serverConn, clientConn := net.Pipe()
	l.accepted <- serverConn
	conns := newConnSet()
	stopCh := make(chan struct{})
	t.Cleanup(func() {
		close(stopCh)
		_ = l.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	done := make(chan struct{})
	go func() {
		serveServiceListener(context.Background(), l, conns, stopCh, gate)
		close(done)
	}()

	// The client is never served: the connection is closed unread — a read
	// fails instead of returning a handshake answer.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := tryReadEnvelope(clientConn); err == nil {
		t.Fatalf("a connection accepted after shutdown began must not be served, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the accept loop must exit after refusing a post-shutdown connection")
	}
	if gate.inflight() != 0 {
		t.Fatal("a post-shutdown connection must not admit handlers")
	}
}

// stubServiceListener hands out queued connections from Accept, so the
// Accept/shutdown race can be staged deterministically.
type stubServiceListener struct {
	accepted  chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *stubServiceListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accepted:
		return c, nil
	case <-l.closed:
		return nil, errors.New("listener closed")
	}
}

func (l *stubServiceListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *stubServiceListener) Addr() net.Addr { return stubServiceAddr{} }

type stubServiceAddr struct{}

func (stubServiceAddr) Network() string { return "stub" }
func (stubServiceAddr) String() string  { return "stub" }

// The full coordinated shutdown through the REAL proxyCoreServiceHandler
// Execute (SCM contract, no SCM): a client Start is held at the global
// handler-slot wait (admission counted, slot not granted), then SCM Stop
// begins. The pending request must be refused without ever reaching
// ServiceStart, the runtime sink must stay untouched even after the slots
// free up, and Stopped must be reported only after the coordinated sequence.
func TestServiceExecuteShutdownRefusesPendingStart(t *testing.T) {
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-ExecuteShutdown`)
	// The DACL carries the runner's own SID alongside SYSTEM/Administrators:
	// creating the SECOND pipe instance (after the first client connected)
	// needs FILE_CREATE_PIPE_INSTANCE on the existing pipe, which a
	// non-elevated runner only has through its own SID ACE (the PC-120
	// installer grants the owner the same way).
	selfSID, _ := currentProcessIdentity(t)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
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

	waitStatus := func(want svc.State) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case s := <-statuses:
				if s.State == want {
					return
				}
			case <-deadline:
				t.Fatalf("service never reached state %d", want)
			}
		}
	}
	waitStatus(svc.Running)
	stoppedSeen := make(chan bool, 1)
	go func() {
		for s := range statuses {
			if s.State == svc.Stopped {
				stoppedSeen <- true
				return
			}
		}
	}()

	conn := dialServicePipe(t)
	defer func() { _ = conn.Close() }()
	mustHandshake(t, conn)

	// Hold the Start at the global-slot wait: saturate the slots, send the
	// request (a config that WOULD start a runtime if dispatched), and wait
	// until the admission gate has counted it — from there the serve loop is
	// parked in the slot wait.
	for i := 0; i < serviceHandlerConcurrencyGlobal; i++ {
		serviceHandlerSlots <- struct{}{}
	}
	envSend(t, conn, 100, serviceProtocolVersion, "Start", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`),
	}))
	waitAdmissionInflight(t, handler.admission, 1)
	if currentBox() != nil {
		t.Fatal("the held request must not have started a runtime before shutdown")
	}

	// SCM Stop begins while the request is parked. Stopped must arrive
	// promptly (the barrier refuses the parked request; the Wait is not
	// blocked by it forever — bounded shutdown).
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
	select {
	case <-stoppedSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the last reported status must be Stopped")
	}
	if currentBox() != nil {
		t.Fatal("the pending Start must never have reached the runtime sink")
	}

	// The client saw the shutdown: either the typed refusal or the close.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := tryReadEnvelope(conn); err == nil && resp.GetCode() != envelopeCodeServiceUnavailable {
		t.Fatalf("a request held at shutdown must be refused with code=%d, got code=%d message=%q",
			envelopeCodeServiceUnavailable, resp.GetCode(), resp.GetMessage())
	}

	// A freed slot must not resurrect the refused request.
	waitAdmissionInflight(t, handler.admission, 0)
	for len(serviceHandlerSlots) > 0 {
		<-serviceHandlerSlots
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if currentBox() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if currentBox() != nil {
		t.Fatal("a freed handler slot must not resurrect the refused Start")
	}
	assertPayloadBudgetIntact(t)
}

// ---- P1: the request deadline acts during the slot waits ----

// A request whose deadline expires while it waits for a GLOBAL handler slot
// is refused with code 5, never dispatched — even after a slot frees up —
// and the connection stays usable. Nothing leaks: no handler was admitted
// and left behind, every global slot comes back, and the aggregate payload
// budget is fully acquirable.
func TestEnvelopeDeadlineExpiresWaitingForGlobalSlot(t *testing.T) {
	conn, gate, _ := serveWithGate(t, context.Background())
	mustHandshake(t, conn)

	for i := 0; i < serviceHandlerConcurrencyGlobal; i++ {
		serviceHandlerSlots <- struct{}{}
	}
	expired := &gen.RequestEnvelope{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		RequestId:       To(uint64(60)),
		Operation:       To("Start"),
		DeadlineUnixMs:  To(time.Now().Add(150 * time.Millisecond).UnixMilli()),
		TypedPayload: mustMarshal(t, &gen.LoadConfigReq{
			CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`),
		}),
	}
	envWrite(t, conn, envFrameMsg(t, expired))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := envRead2(conn)
	if err != nil {
		t.Fatalf("read deadline answer: %v", err)
	}
	if resp.GetCode() != envelopeCodeDeadlineExceeded || !strings.HasPrefix(resp.GetMessage(), errDeadlineExceeded) {
		t.Fatalf("a deadline expired in the slot queue must be code=%d %s, got code=%d message=%q",
			envelopeCodeDeadlineExceeded, errDeadlineExceeded, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 60 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 60", resp.GetRequestId())
	}
	if currentBox() != nil {
		t.Fatal("a request whose deadline expired in the queue must not reach the runtime")
	}

	// Free a slot: the expired request must not be resurrected, and the
	// connection must keep serving.
	<-serviceHandlerSlots
	if resp := envCall(t, conn, 61, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after the queued-deadline refusal must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if currentBox() != nil {
		t.Fatal("the freed slot must not have dispatched the expired Start")
	}

	// Nothing leaks: every spawned handler has finished (so its slot
	// release already happened), the test's saturation tokens drain, and
	// the aggregate payload budget is fully acquirable again.
	waitAdmissionInflight(t, gate, 0)
	for len(serviceHandlerSlots) > 0 {
		<-serviceHandlerSlots
	}
	waitGlobalSlotsIdle(t)
	assertPayloadBudgetIntact(t)
}

// blockingHealth replaces the registry's Health with a handler that parks
// until released, so a connection's per-connection handler limit can be
// saturated deterministically. The returned func releases the parked
// handlers (idempotent; also called on cleanup, which then restores the
// registry).
func blockingHealth(t *testing.T) (release func()) {
	t.Helper()
	original := serviceOperations["Health"]
	releaseCh := make(chan struct{})
	var once sync.Once
	serviceOperations["Health"] = serviceOperationDef{
		name: "Health",
		decode: func(payload []byte) (proto.Message, error) {
			req := &gen.EmptyReq{}
			if err := proto.Unmarshal(payload, req); err != nil {
				return nil, err
			}
			if unknown := req.ProtoReflect().GetUnknown(); len(unknown) > 0 {
				return nil, errors.New("payload carries fields unknown to Health")
			}
			return req, nil
		},
		call: func(ctx context.Context, msg proto.Message) (proto.Message, error) {
			<-releaseCh
			return &gen.HealthResp{RuntimeRunning: To(false), ProtocolVersion: To(int32(serviceProtocolVersion))}, nil
		},
	}
	t.Cleanup(func() {
		once.Do(func() { close(releaseCh) })
		serviceOperations["Health"] = original
	})
	return func() { once.Do(func() { close(releaseCh) }) }
}

// A request whose deadline expires while it waits for a PER-CONNECTION
// handler slot (eight in-flight handlers hold them) is refused with code 5
// and the preserved request id, the eight real handlers still complete, and
// nothing leaks: the per-connection limit, the global slots, the admission
// count and the payload budget all return to their baseline.
func TestEnvelopeDeadlineExpiresWaitingForPerConnSlot(t *testing.T) {
	conn, gate, _ := serveWithGate(t, context.Background())
	mustHandshake(t, conn)

	release := blockingHealth(t)

	// Eight in-flight handlers hold the per-connection limit (and eight
	// global slots). The ids start past the handshake's id (the handshake
	// consumes its own id in the dedup window).
	for i := uint64(100); i < 100+serviceHandlerConcurrencyPerConn; i++ {
		envSend(t, conn, i, serviceProtocolVersion, "Health", nil)
	}
	waitAdmissionInflight(t, gate, int(serviceHandlerConcurrencyPerConn))

	// The ninth parks in the per-connection wait and its deadline expires
	// there.
	ninth := &gen.RequestEnvelope{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		RequestId:       To(uint64(9)),
		Operation:       To("Health"),
		DeadlineUnixMs:  To(time.Now().Add(200 * time.Millisecond).UnixMilli()),
	}
	envWrite(t, conn, envFrameMsg(t, ninth))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := envRead2(conn)
	if err != nil {
		t.Fatalf("read ninth answer: %v", err)
	}
	if resp.GetCode() != envelopeCodeDeadlineExceeded || !strings.HasPrefix(resp.GetMessage(), errDeadlineExceeded) {
		t.Fatalf("a deadline expired in the per-connection queue must be code=%d %s, got code=%d message=%q",
			envelopeCodeDeadlineExceeded, errDeadlineExceeded, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 9 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 9", resp.GetRequestId())
	}

	// Release the eight handlers: exactly eight OK answers come back — the
	// ninth never executed — and the connection keeps serving.
	release()
	for i := 0; i < int(serviceHandlerConcurrencyPerConn); i++ {
		r := envRead(t, conn)
		if r.GetCode() != envelopeCodeOK || r.GetRequestId() == 9 {
			t.Fatalf("expected an OK from one of the eight parked Health handlers, got code=%d id=%d", r.GetCode(), r.GetRequestId())
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	if resp := envCall(t, conn, 10, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after the per-connection refusal must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}

	// Nothing leaks: the eight handlers finished, the test's saturation
	// tokens drain, and the payload budget is fully acquirable again.
	waitAdmissionInflight(t, gate, 0)
	for len(serviceHandlerSlots) > 0 {
		<-serviceHandlerSlots
	}
	waitGlobalSlotsIdle(t)
	assertPayloadBudgetIntact(t)
}

// Shutdown also aborts a request parked in the PER-CONNECTION wait: code 7,
// the serve loop finishes, and the parked handlers wind down without
// dispatching anything new.
func TestEnvelopeShutdownRefusesPendingPerConnWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, gate, finished := serveWithGate(t, ctx)
	mustHandshake(t, conn)

	release := blockingHealth(t)

	for i := uint64(100); i < 100+serviceHandlerConcurrencyPerConn; i++ {
		envSend(t, conn, i, serviceProtocolVersion, "Health", nil)
	}
	waitAdmissionInflight(t, gate, int(serviceHandlerConcurrencyPerConn))

	envSend(t, conn, 20, serviceProtocolVersion, "Health", nil) // parks in the per-conn wait
	cancel()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := envRead2(conn)
	if err != nil {
		t.Fatalf("read shutdown answer: %v", err)
	}
	if resp.GetCode() != envelopeCodeServiceUnavailable {
		t.Fatalf("a request parked past shutdown must be refused with code=%d, got code=%d message=%q",
			envelopeCodeServiceUnavailable, resp.GetCode(), resp.GetMessage())
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the serve loop must finish when shutdown aborts a per-connection wait")
	}
	if currentBox() != nil {
		t.Fatal("shutdown must not have executed the parked request")
	}

	release()
	waitAdmissionInflight(t, gate, 0)
	for len(serviceHandlerSlots) > 0 {
		<-serviceHandlerSlots
	}
	waitGlobalSlotsIdle(t)
	assertPayloadBudgetIntact(t)
}

// ---- P2: the Hello obeys the general envelope contract ----

// The handshake obeys the request_id rule: id 0 is its absence and is
// refused as a typed invalid request, without breaking the connection — the
// client can still complete the handshake afterwards.
func TestEnvelopeHelloZeroRequestIDRefused(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	envWrite(t, conn, envFrame(t, 0, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{})))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("a Hello with request_id 0 must be code=%d %s, got code=%d message=%q",
			envelopeCodeInvalidRequest, errInvalidRequest, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 0 {
		t.Fatalf("the echo of an absent id is 0, got %d", resp.GetRequestId())
	}
	if resp := envHelloID(t, conn, 1); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("the handshake must still be completable, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// An expired Hello is refused before anything executes, preserves the
// request id, and does NOT establish the handshake.
func TestEnvelopeHelloExpiredDeadlineNoHandshake(t *testing.T) {
	conn, finished := serveEnvelopeContextOverPipe(t, context.Background())
	envWrite(t, conn, envFrameMsg(t, envExpiredHello(t, 5)))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeDeadlineExceeded || !strings.HasPrefix(resp.GetMessage(), errDeadlineExceeded) {
		t.Fatalf("an expired Hello must be code=%d %s, got code=%d message=%q",
			envelopeCodeDeadlineExceeded, errDeadlineExceeded, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 5 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 5", resp.GetRequestId())
	}

	// Without a handshake, a regular request gets the typed no-handshake
	// refusal and the server closes the connection.
	if resp := envCall(t, conn, 6, "Health", nil); resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errNoHandshake) {
		t.Fatalf("a request before any handshake must be refused with %s, got code=%d message=%q",
			errNoHandshake, resp.GetCode(), resp.GetMessage())
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a non-Hello first request must close the connection")
	}
}

// An expired Hello consumes nothing: the same request id can be retried and
// then completes the handshake.
func TestEnvelopeHelloExpiredDeadlineRetryAllowed(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	envWrite(t, conn, envFrameMsg(t, envExpiredHello(t, 5)))
	if resp := envRead(t, conn); resp.GetCode() != envelopeCodeDeadlineExceeded {
		t.Fatalf("an expired Hello must be refused with code=%d, got code=%d", envelopeCodeDeadlineExceeded, resp.GetCode())
	}
	if resp := envHelloID(t, conn, 5); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("a retried Hello with the same id must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if resp := envCall(t, conn, 6, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after the retried handshake must succeed, got code=%d", resp.GetCode())
	}
}

// The stale-revision gate covers the handshake too: a Hello pinning a
// revision the service does not run is refused (code 6) and can be retried
// on the same connection with the current revision.
func TestEnvelopeHelloStalePolicyRevisionRefused(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	if configPolicyRevision < 1 {
		t.Fatalf("configPolicyRevision = %d, want >= 1", configPolicyRevision)
	}
	stale := &gen.RequestEnvelope{
		ProtocolVersion:        To(int32(serviceProtocolVersion)),
		RequestId:              To(uint64(3)),
		Operation:              To("Hello"),
		ExpectedPolicyRevision: To(int64(configPolicyRevision) + 1),
		TypedPayload:           mustMarshal(t, &gen.HandshakeReq{}),
	}
	envWrite(t, conn, envFrameMsg(t, stale))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeStalePolicyRevision || !strings.HasPrefix(resp.GetMessage(), errStalePolicyRevision) {
		t.Fatalf("a stale-revision Hello must be code=%d %s, got code=%d message=%q",
			envelopeCodeStalePolicyRevision, errStalePolicyRevision, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 3 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 3", resp.GetRequestId())
	}

	current := &gen.RequestEnvelope{
		ProtocolVersion:        To(int32(serviceProtocolVersion)),
		RequestId:              To(uint64(3)),
		Operation:              To("Hello"),
		ExpectedPolicyRevision: To(int64(configPolicyRevision)),
		TypedPayload:           mustMarshal(t, &gen.HandshakeReq{}),
	}
	envWrite(t, conn, envFrameMsg(t, current))
	if resp := envRead(t, conn); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("a Hello with the current revision must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// The handshake's request id joins the connection's dedup window the moment
// the handshake is accepted (documented semantics): a later request reusing
// it gets the duplicate code, a fresh id is served.
func TestEnvelopeHandshakeIDConsumedByDedup(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	if resp := envHelloID(t, conn, 1); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("handshake failed: code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if resp := envCall(t, conn, 1, "Health", nil); resp.GetCode() != envelopeCodeDuplicateRequest || !strings.HasPrefix(resp.GetMessage(), errDuplicateRequest) {
		t.Fatalf("reusing the handshake id must be code=%d %s, got code=%d message=%q",
			envelopeCodeDuplicateRequest, errDuplicateRequest, resp.GetCode(), resp.GetMessage())
	}
	if resp := envCall(t, conn, 2, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health with a fresh id must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// The envelope's protocol_version is the single version authority: the
// handshake payload's own version field is documented as ignored, so a Hello
// that carries a mismatched payload version still completes (the envelope
// gate has already refused incompatible clients before any dispatch).
func TestEnvelopeHelloPayloadVersionNotConsulted(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	envSend(t, conn, 1, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{ProtocolVersion: To(int32(99))}))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("the handshake payload version must not be consulted, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	health := &gen.HealthResp{}
	if resp := envCall(t, conn, 2, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after the handshake must succeed, got code=%d", resp.GetCode())
	} else if err := proto.Unmarshal(resp.GetTypedPayload(), health); err != nil {
		t.Fatal(err)
	}
}

// Shutdown refuses a new handshake. With the serve context already canceled,
// the frame's payload-budget gate aborts before the body is read (documented
// readServiceEnvelope behavior: a canceled context answers nothing), so the
// deterministic assertions are: no handshake is established, the client never
// receives a successful answer, and the connection closes. The typed code-7
// refusal for an already-read frame exercises the same serve-loop gate and is
// covered by the pending-handler shutdown tests.
func TestEnvelopeShutdownRefusesHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, finished := serveEnvelopeContextOverPipe(t, ctx)
	cancel()
	// The server may close mid-write (it aborts after the 4-byte header);
	// the write error, if any, is the refusal showing through.
	_, _ = conn.Write(envFrame(t, 1, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{})))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := envRead2(conn); err == nil && resp.GetCode() == envelopeCodeOK {
		t.Fatal("shutdown must not establish a handshake, got an OK answer")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown must close a connection whose handshake it refused")
	}
}

// ---- remediation round 3 (2026-09-13 review): shutdown vs runtime creation,
// the first Hello behind the final gate, the handshake budget leak ----

// parkingHello replaces the registry's Hello entry with a pass-through whose
// decode parks until released — the deterministic staging point AFTER the
// frame is read, validated and decoded and BEFORE the dispatch (the exact
// window the final handshake gate must cover). The recorded `called`
// reports whether the registry entry — and with it the real globalServer.Hello
// it forwards to — ever executed. Only the FIRST Hello parks; later frames
// (a corrected retry) pass straight through.
func parkingHello(t *testing.T) (arrived <-chan struct{}, release func(), called func() bool) {
	t.Helper()
	original := serviceOperations["Hello"]
	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	parked := false
	invoked := false
	serviceOperations["Hello"] = serviceOperationDef{
		name: "Hello",
		decode: func(payload []byte) (proto.Message, error) {
			req := &gen.HandshakeReq{}
			if err := proto.Unmarshal(payload, req); err != nil {
				return nil, err
			}
			if unknown := req.ProtoReflect().GetUnknown(); len(unknown) > 0 {
				return nil, errors.New("payload carries fields unknown to Hello")
			}
			if !parked {
				parked = true
				close(entered)
				<-releaseCh
			}
			return req, nil
		},
		call: func(ctx context.Context, msg proto.Message) (proto.Message, error) {
			invoked = true
			return original.call(ctx, msg)
		},
	}
	t.Cleanup(func() {
		once.Do(func() { close(releaseCh) })
		serviceOperations["Hello"] = original
	})
	return entered, func() { once.Do(func() { close(releaseCh) }) }, func() bool { return invoked }
}

// The Start-vs-shutdown race the slot-wait staging cannot reach: a client
// Start that has ALREADY passed handler admission, both slot waits and the
// request-context re-check — it is suspended inside ServiceStart, after every
// validation and before the runtime-creation critical section (the pause
// seam; the legacy lifecycleMu capture inside Start happens even later). The
// SCM-style Stop completes WHILE the Start is suspended: the shutdown mark is
// raised under serviceRuntimeMu, the final Stop finds no runtime, Stopped is
// published. The resumed Start must then observe the mark and refuse — the
// runtime sink (boxmain.Create, the Xray instances, the extra process) is
// never reached, and no runtime, Xray or extra process exists after Stopped.
func TestServiceExecuteStoppedRefusesSuspendedStart(t *testing.T) {
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-ExecuteSuspendedStart`)
	selfSID, _ := currentProcessIdentity(t)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
	resetServiceRuntimeStoppingForTest(t)
	// If the mark were not checked, the resumed Start would create a real
	// runtime; the cleanup keeps such a failure from poisoning the suite.
	t.Cleanup(func() {
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
	})

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	prevPause := serviceStartPause
	serviceStartPause = func() {
		close(entered)
		<-releaseCh
	}
	t.Cleanup(func() {
		once.Do(func() { close(releaseCh) })
		serviceStartPause = prevPause
	})

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
	waitStatus := func(want svc.State) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case s := <-statuses:
				if s.State == want {
					return
				}
			case <-deadline:
				t.Fatalf("service never reached state %d", want)
			}
		}
	}
	waitStatus(svc.Running)
	stoppedSeen := make(chan bool, 1)
	go func() {
		for s := range statuses {
			if s.State == svc.Stopped {
				stoppedSeen <- true
				return
			}
		}
	}()

	conn := dialServicePipe(t)
	defer func() { _ = conn.Close() }()
	mustHandshake(t, conn)

	envSend(t, conn, 100, serviceProtocolVersion, "Start", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`),
	}))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the Start never reached ServiceStart's runtime-creation phase")
	}
	// Past admission, past both slot waits, past the request-context
	// re-check: the handler is admitted and executing ServiceStart.
	waitAdmissionInflight(t, handler.admission, 1)

	// SCM-style Stop while the runtime-creating Start is suspended.
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case fire := <-exited:
		if fire {
			t.Fatal("a normal SCM stop must not request service death")
		}
		if code := <-exitCode; code != 0 {
			t.Fatalf("normal stop exit code = %d, want 0", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Execute did not return after a Stop request")
	}
	select {
	case <-stoppedSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the last reported status must be Stopped")
	}

	// Stopped was published while the Start is STILL suspended.
	if handler.admission.inflight() != 1 {
		t.Fatalf("the suspended Start must still be in flight at Stopped, inflight = %d", handler.admission.inflight())
	}
	if !serviceRuntimeStopping {
		t.Fatal("the stop path must have raised the runtime shutdown mark")
	}
	if currentBox() != nil {
		t.Fatal("Stopped must be published with no runtime")
	}

	// Let the Start continue: it must observe the mark and refuse — the
	// runtime sink is never reached, and nothing appears after Stopped.
	once.Do(func() { close(releaseCh) })
	waitAdmissionInflight(t, handler.admission, 0)
	if currentBox() != nil {
		t.Fatal("a Start resumed after Stopped must be refused by the shutdown mark: the runtime sink was reached")
	}
	if extraProcess != nil {
		t.Fatal("no extra process may exist after Stopped")
	}
	if instances := liveXrayInstances(); len(instances) != 0 {
		t.Fatalf("no Xray instance may exist after Stopped, got %d", len(instances))
	}
	assertPayloadBudgetIntact(t)
}

// The first Hello behind the final gate — shutdown: the Hello is read,
// validated and decoded (parked at the staging point), THEN shutdown begins.
// After the barrier is lifted the serve loop must refuse the decoded Hello
// without dispatching it — the registry entry, and the real globalServer.Hello
// behind it, is never called; no OK is returned, no handshake is established,
// and the connection closes.
func TestEnvelopeHelloShutdownAfterDecodeNoDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, finished := serveEnvelopeContextOverPipe(t, ctx)
	arrived, release, called := parkingHello(t)

	envSend(t, conn, 1, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{}))
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the Hello never reached the dispatch staging point")
	}
	cancel() // shutdown begins while the decoded Hello waits for dispatch
	release()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := envRead2(conn)
	if err != nil {
		t.Fatalf("read shutdown refusal: %v", err)
	}
	if resp.GetCode() != envelopeCodeServiceUnavailable || resp.GetMessage() != errServiceStopping.Error() {
		t.Fatalf("a Hello decoded after shutdown began must be refused with code=%d, got code=%d message=%q",
			envelopeCodeServiceUnavailable, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 1 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 1", resp.GetRequestId())
	}
	if called() {
		t.Fatal("the registry Hello must not be called after shutdown began")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the serve loop must close a connection whose decoded handshake it refused")
	}
	assertPayloadBudgetIntact(t)
}

// The first Hello behind the final gate — deadline: the Hello's deadline
// expires AFTER the serve loop's deadline check (while the decoded frame
// waits at the staging point) and BEFORE the dispatch. The registry entry is
// never called, no handshake is established, no OK is returned, and the
// connection stays usable: the id was consumed by the acceptance, so the
// same-id retry gets code 9 and a fresh id completes the handshake.
func TestEnvelopeHelloDeadlineExpiredBeforeDispatch(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	arrived, release, called := parkingHello(t)

	hello := &gen.RequestEnvelope{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		RequestId:       To(uint64(1)),
		Operation:       To("Hello"),
		DeadlineUnixMs:  To(time.Now().Add(300 * time.Millisecond).UnixMilli()),
		TypedPayload:    mustMarshal(t, &gen.HandshakeReq{}),
	}
	envWrite(t, conn, envFrameMsg(t, hello))
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the Hello never reached the dispatch staging point")
	}
	// Let the deadline expire deterministically while the frame is parked.
	deadline := time.UnixMilli(hello.GetDeadlineUnixMs())
	if !time.Now().Before(deadline) {
		t.Fatal("the deadline expired before the frame was decoded — staging window wrong")
	}
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	release()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeDeadlineExceeded || !strings.HasPrefix(resp.GetMessage(), errDeadlineExceeded) {
		t.Fatalf("a Hello whose deadline expired before dispatch must be code=%d %s, got code=%d message=%q",
			envelopeCodeDeadlineExceeded, errDeadlineExceeded, resp.GetCode(), resp.GetMessage())
	}
	if resp.GetRequestId() != 1 {
		t.Fatalf("the refusal must preserve the request_id, got %d want 1", resp.GetRequestId())
	}
	if called() {
		t.Fatal("the registry Hello must not be called after the deadline expired")
	}

	// The connection stays usable; the consumed id documents the same
	// conservative rule as a request refused after admission.
	if resp := envCall(t, conn, 1, "Hello", mustMarshal(t, &gen.HandshakeReq{})); resp.GetCode() != envelopeCodeDuplicateRequest {
		t.Fatalf("the accepted-but-refused Hello keeps its id consumed: want code=%d, got code=%d message=%q",
			envelopeCodeDuplicateRequest, resp.GetCode(), resp.GetMessage())
	}
	if resp := envHelloID(t, conn, 2); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("a fresh-id retry must complete the handshake, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if resp := envCall(t, conn, 3, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after the retried handshake must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	// The Health handler's weight is released by the spawned goroutine's
	// defers, slightly after its response — drain the slots before checking
	// the budget.
	waitGlobalSlotsIdle(t)
	assertPayloadBudgetIntact(t)
}

// The Stop-vs-Start race the pause seam cannot reach (F7): a Start that has
// ALREADY passed the barrier pre-check and is blocked INSIDE the
// runtime-creation delegation while SCM Stop runs. The stop path must
// complete promptly — the mark acquisition is O(1) and never waits for the
// in-flight creation — and when the blocked Start finishes it must observe
// the mark, tear down, and refuse with code 7 instead of publishing a
// post-Stopped runtime. Against the previous design (barrier lock held
// across the delegation) this test hangs at Stop and fails by timeout.
func TestServiceExecuteStopBoundedWithStartInsideCreation(t *testing.T) {
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-ExecuteInsideCreation`)
	selfSID, _ := currentProcessIdentity(t)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
	resetServiceRuntimeStoppingForTest(t)

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	prevDelegate := serviceStartDelegate
	serviceStartDelegate = func(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
		enterOnce.Do(func() { close(entered) })
		<-releaseCh
		// Simulate "creation finished after Stopped": the sink itself is left
		// untouched (no box/Xray/extra process), success models a Start that
		// WOULD have published a runtime — the post-check must still refuse.
		return &gen.ErrorResp{}, nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		serviceStartDelegate = prevDelegate
	})

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
	waitStatus := func(want svc.State) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case s := <-statuses:
				if s.State == want {
					return
				}
			case <-deadline:
				t.Fatalf("service never reached state %d", want)
			}
		}
	}
	waitStatus(svc.Running)
	stoppedSeen := make(chan bool, 1)
	go func() {
		for s := range statuses {
			if s.State == svc.Stopped {
				stoppedSeen <- true
				return
			}
		}
	}()

	conn := dialServicePipe(t)
	defer func() { _ = conn.Close() }()
	mustHandshake(t, conn)

	envSend(t, conn, 100, serviceProtocolVersion, "Start", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`),
	}))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the Start never reached the runtime-creation delegation")
	}
	// NOTE: no barrier-counter assertion here: under the pre-fix design the
	// creation holds the barrier lock, so even reading the counter would
	// block — `entered` alone proves the staging point was reached.

	// SCM-style Stop while the creation is still blocked inside. The stop
	// path must NOT wait for it: Stopped arrives on the bounded path (the
	// 2s handlers wait bounds it even with the Start parked).
	stopSent := time.Now()
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
		t.Fatal("Execute did not return after a Stop request: the stop path waited for the in-flight creation (F7)")
	}
	if elapsed := time.Since(stopSent); elapsed > 10*time.Second {
		t.Fatalf("Stop took %v with a Start parked inside creation: shutdown is not bounded (F7)", elapsed)
	}
	select {
	case <-stoppedSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the last reported status must be Stopped")
	}

	// Stopped was published while the creation is STILL blocked inside.
	if !serviceRuntimeStopping {
		t.Fatal("the stop path must have raised the runtime shutdown mark")
	}
	if currentBox() != nil {
		t.Fatal("Stopped must be published with no runtime")
	}

	// Let the creation finish past the mark. The client connection was
	// dropped by the stop path's closeAll, so the refusal has nobody to
	// answer (the handler's write fails silently) — what must hold is the
	// self-teardown: no runtime persists, and the barrier accounting drains.
	// The refusal VALUE itself (code 7) is pinned by the direct post-check
	// test below, where no connection is involved.
	releaseOnce.Do(func() { close(releaseCh) })
	waitAdmissionInflight(t, handler.admission, 0)
	deadline := time.Now().Add(5 * time.Second)
	for serviceRuntimeInflight() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := serviceRuntimeInflight(); got != 0 {
		t.Fatalf("runtime-creation inflight = %d, want 0 (leaked barrier count)", got)
	}
	if currentBox() != nil {
		t.Fatal("no runtime may exist after the post-mark teardown")
	}
	if extraProcess != nil {
		t.Fatal("no extra process may exist after the post-mark teardown")
	}
	if instances := liveXrayInstances(); len(instances) != 0 {
		t.Fatalf("no Xray instance may exist after the post-mark teardown, got %d", len(instances))
	}
	assertPayloadBudgetIntact(t)
}

// The ServiceStart post-check, directly: a creation that finishes past the
// shutdown mark must tear down and refuse with errServiceStopping (envelope
// code 7) — even though the delegation itself reported success (i.e. it
// WOULD have published a runtime). No pipe or SCM involved.
func TestServiceStartPostCheckRefusesAfterMark(t *testing.T) {
	resetServiceRuntimeBarrier()
	t.Cleanup(resetServiceRuntimeBarrier)

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	prevDelegate := serviceStartDelegate
	serviceStartDelegate = func(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
		enterOnce.Do(func() { close(entered) })
		<-releaseCh
		return &gen.ErrorResp{}, nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		serviceStartDelegate = prevDelegate
	})
	t.Cleanup(func() {
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := globalServer.ServiceStart(context.Background(), &gen.LoadConfigReq{
			CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`),
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("ServiceStart never reached the runtime-creation delegation")
	}
	// The shutdown mark lands while the creation is still inside.
	beginServiceRuntimeShutdown()
	releaseOnce.Do(func() { close(releaseCh) })

	select {
	case err := <-done:
		if !errors.Is(err, errServiceStopping) {
			t.Fatalf("a Start finished past the mark must refuse with errServiceStopping, got %v", err)
		}
		if code := envelopeCodeForHandlerError(err); code != envelopeCodeServiceUnavailable {
			t.Fatalf("the post-mark refusal must map to code=%d, got code=%d",
				envelopeCodeServiceUnavailable, code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServiceStart did not return after the mark and the delegation finished")
	}
	if got := serviceRuntimeInflight(); got != 0 {
		t.Fatalf("runtime-creation inflight = %d, want 0", got)
	}
	if currentBox() != nil {
		t.Fatal("no runtime may exist after the post-mark teardown")
	}
}

// The handshake's payload budget: a valid Hello whose ANSWER write fails —
// the client closes the connection right after the answer's length header,
// while the server's write is still in flight on the synchronous pipe — must
// release the request frame's weight. Repeated, because a per-connection leak
// is exactly an accumulating one: the full-budget acquire fails on ANY
// stranded weight. (A strictly-valid Hello cannot be padded — the strict
// decode refuses unknown-field residue — so the frame is the one the
// handshake actually allocates; the accounting is what the test pins.)
func TestEnvelopeHandshakeAnswerWriteFailureReleasesBudget(t *testing.T) {
	for i := 0; i < 8; i++ {
		conn, finished := serveEnvelopeContextOverPipe(t, context.Background())
		envSend(t, conn, 1, serviceProtocolVersion, "Hello", mustMarshal(t, &gen.HandshakeReq{}))

		// Read ONLY the answer's length header: the handshake dispatched and
		// its answer write is in flight.
		var header [4]byte
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			t.Fatalf("iteration %d: read answer header: %v", i, err)
		}
		// Close before the body: the pending answer write fails.
		_ = conn.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: the serve loop must finish when the handshake answer write fails", i)
		}
		assertPayloadBudgetIntact(t)
	}
	if currentBox() != nil {
		t.Fatal("no runtime may exist")
	}
}
