//go:build windows

// PC-110 envelope-contract tests: version handling, unknown operations, wrong
// payloads, size limits, deadlines, policy revisions, duplicate request ids
// and shutdown semantics — every refusal typed, every refusal documented as
// either connection-preserving or disconnecting. All in-process (net.Pipe).

package main

import (
	"ThroneCore/gen"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// serveEnvelopeContextOverPipe drives serveServiceConnContext with the given
// context (the shutdown tests need to cancel it) and this connection's own
// admission gate.
func serveEnvelopeContextOverPipe(t *testing.T, ctx context.Context) (client net.Conn, finished <-chan struct{}) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		serveServiceConnContext(ctx, serverConn, newServiceHandlerGate())
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
	return clientConn, done
}

// ---- protocol version ----

// The version gate applies to EVERY envelope, not only the handshake: a
// client that starts incompatible mid-connection is answered and dropped.
func TestEnvelopeVersionRequiredOnEveryRequest(t *testing.T) {
	conn, finished := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)
	envSend(t, conn, 2, serviceProtocolVersion+1, "Health", nil)
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeIncompatibleVersion || !strings.HasPrefix(resp.GetMessage(), errProtocolVersion) {
		t.Fatalf("want %s code=%d, got code=%d message=%q", errProtocolVersion, envelopeCodeIncompatibleVersion, resp.GetCode(), resp.GetMessage())
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a client that changes its version mid-connection must be disconnected")
	}
}

// ---- handshake state ----

// A second Hello after a completed handshake is refused without executing it
// again, and the connection stays usable.
func TestEnvelopeHelloAfterHandshakeRefused(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	resp := envCall(t, conn, 2, "Hello", nil)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errNoHandshake) {
		t.Fatalf("want %s code=%d, got code=%d message=%q", errNoHandshake, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	if resp := envCall(t, conn, 3, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after a refused duplicate Hello must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// ---- operation resolution and payload typing ----

// A payload that does not decode into the operation's request type is a typed
// invalid request — never a handler crash — and the connection stays usable.
// Strictness matters: protobuf would silently park another message type's
// fields (or a newer client's fields) in unknown fields, and the registry
// refuses any such residue, so an operation only accepts fields it declares.
func TestEnvelopeWrongPayloadTypedError(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	// HandshakeReq bytes (field 1, wire type 0) against LoadConfigReq, whose
	// field 1 is a string: protobuf parks the mismatch in unknown fields and
	// the registry must refuse it.
	handshakeBytes := mustMarshal(t, &gen.HandshakeReq{ProtocolVersion: To(int32(7))})
	resp := envCall(t, conn, 10, "Start", handshakeBytes)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("a wrong payload type must be %s code=%d, got code=%d message=%q", errInvalidRequest, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	// Raw garbage under a valid length prefix: the envelope decodes the frame,
	// the operation payload does not.
	resp = envCall(t, conn, 11, "CheckConfig", []byte{0xde, 0xad, 0xbe, 0xef, 0xff})
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("garbage payload must be %s code=%d, got code=%d message=%q", errInvalidRequest, envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	if resp := envCall(t, conn, 12, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after wrong payloads must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if currentBox() != nil {
		t.Fatal("wrong payloads must not have started a runtime")
	}
}

// A structurally valid envelope whose bytes are not protobuf at all is
// refused, the answer carries request_id 0 (nothing was parsable), and the
// stream stays parsable because the framing is length-delimited.
func TestEnvelopeMalformedEnvelopeKeepsConnection(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	body := []byte("this is not protobuf")
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	envWrite(t, conn, frame)

	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("a malformed envelope must be code=%d, got code=%d message=%q", envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	if resp := envCall(t, conn, 2, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after a malformed envelope must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// ---- size limits ----

// End to end: an oversized envelope is answered with the typed frame-too-large
// code and the connection is closed — the claimed body was never read, so the
// stream position is unknown.
func TestEnvelopeOversizedWireAnswer(t *testing.T) {
	conn, finished := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], serviceMaxEnvelopeLen+1)
	envWrite(t, conn, header[:])

	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeFrameTooLarge || !strings.HasPrefix(resp.GetMessage(), errFrameTooLarge) {
		t.Fatalf("want %s code=%d, got code=%d message=%q", errFrameTooLarge, envelopeCodeFrameTooLarge, resp.GetCode(), resp.GetMessage())
	}
	// The service closes after the typed answer: the next read must fail.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := envRead2(conn); err == nil {
		t.Fatal("the connection must be closed after a frame-too-large answer")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the service must close the connection after an oversized frame")
	}
}

// envRead2 is envRead without the t.Fatal-on-error contract, for reads whose
// failure is the expected outcome.
func envRead2(r io.Reader) (*gen.ResponseEnvelope, error) {
	var lenBytes [4]byte
	if _, err := io.ReadFull(r, lenBytes[:]); err != nil {
		return nil, err
	}
	body := make([]byte, binary.LittleEndian.Uint32(lenBytes[:]))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	resp := &gen.ResponseEnvelope{}
	if err := proto.Unmarshal(body, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ---- deadlines ----

// An already-expired deadline is refused before dispatch: the request never
// reaches a handler (a Start that WOULD have produced ERR_INVALID_REQUEST
// produces the deadline code instead), and the connection stays usable.
func TestEnvelopeExpiredDeadlineNotDispatched(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	expired := &gen.RequestEnvelope{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		RequestId:       To(uint64(20)),
		Operation:       To("Start"),
		DeadlineUnixMs:  To(time.Now().Add(-time.Hour).UnixMilli()),
		TypedPayload: mustMarshal(t, &gen.LoadConfigReq{
			NeedXray:   proto.Bool(true),
			CoreConfig: proto.String(`{"inbounds":[],"outbounds":[]}`),
		}),
	}
	envWrite(t, conn, envFrameMsg(t, expired))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeDeadlineExceeded || !strings.HasPrefix(resp.GetMessage(), errDeadlineExceeded) {
		t.Fatalf("an expired deadline must be code=%d %s, got code=%d message=%q", envelopeCodeDeadlineExceeded, errDeadlineExceeded, resp.GetCode(), resp.GetMessage())
	}
	if currentBox() != nil {
		t.Fatal("an expired-deadline request must not have reached the runtime")
	}

	// The connection stays usable.
	if resp := envCall(t, conn, 21, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after an expired deadline must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// envFrameMsg frames an already-built RequestEnvelope (the deadline tests need
// envelope fields the generic helper does not set).
func envFrameMsg(t *testing.T, env *gen.RequestEnvelope) []byte {
	t.Helper()
	body, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	return frame
}

// envelopeContext must bind the handler context to the client deadline when
// one is sent and leave it unbounded (connection-scoped) when none is.
func TestEnvelopeDeadlineContext(t *testing.T) {
	env := &gen.RequestEnvelope{DeadlineUnixMs: To(time.Now().Add(50 * time.Millisecond).UnixMilli())}
	ctx, cancel := envelopeContext(context.Background(), env)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a sent deadline must bound the handler context")
	}
	if time.Until(deadline) > time.Minute {
		t.Fatalf("handler deadline is unreasonably far out: %v", deadline)
	}
	time.Sleep(80 * time.Millisecond)
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("the handler context must expire, got %v", ctx.Err())
	}

	plain, cancelPlain := envelopeContext(context.Background(), &gen.RequestEnvelope{})
	defer cancelPlain()
	if _, ok := plain.Deadline(); ok {
		t.Fatal("an absent deadline must not bound the handler context")
	}
	if plain.Err() != nil {
		t.Fatalf("an unbounded handler context must be alive, got %v", plain.Err())
	}
}

// ---- expected policy revision ----

// A client pinning a config-policy revision that the service no longer runs
// gets the typed stale answer (naming the current revision) instead of a
// shifted acceptance decision, and the connection stays usable. A client
// pinning the current revision is served normally.
func TestEnvelopeStalePolicyRevision(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)
	if configPolicyRevision < 1 {
		t.Fatalf("configPolicyRevision = %d, want >= 1", configPolicyRevision)
	}

	stale := &gen.RequestEnvelope{
		ProtocolVersion:        To(int32(serviceProtocolVersion)),
		RequestId:              To(uint64(30)),
		Operation:              To("CheckConfig"),
		ExpectedPolicyRevision: To(int64(configPolicyRevision) + 1),
		TypedPayload:           mustMarshal(t, &gen.LoadConfigReq{}),
	}
	envWrite(t, conn, envFrameMsg(t, stale))
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeStalePolicyRevision || !strings.HasPrefix(resp.GetMessage(), errStalePolicyRevision) {
		t.Fatalf("want stale-revision code=%d %s, got code=%d message=%q", envelopeCodeStalePolicyRevision, errStalePolicyRevision, resp.GetCode(), resp.GetMessage())
	}
	if !strings.Contains(resp.GetMessage(), fmt.Sprintf("revision %d", configPolicyRevision+1)) ||
		!strings.Contains(resp.GetMessage(), fmt.Sprintf("service runs %d", configPolicyRevision)) {
		t.Fatalf("the stale answer must name both revisions (client %d, service %d): %q", configPolicyRevision+1, configPolicyRevision, resp.GetMessage())
	}

	// The current revision is accepted and the connection still serves.
	current := &gen.RequestEnvelope{
		ProtocolVersion:        To(int32(serviceProtocolVersion)),
		RequestId:              To(uint64(31)),
		Operation:              To("Health"),
		ExpectedPolicyRevision: To(int64(configPolicyRevision)),
	}
	envWrite(t, conn, envFrameMsg(t, current))
	resp = envRead(t, conn)
	if resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health with the current revision must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}

	// A stale-revision request is not recorded as executed: the same id may
	// be retried once the client refreshed its revision.
	retry := envCall(t, conn, 30, "Health", nil)
	if retry.GetCode() != envelopeCodeOK {
		t.Fatalf("a retry with the same id after a stale refusal must be served, got code=%d message=%q", retry.GetCode(), retry.GetMessage())
	}
}

// ---- request id: echo, requirement, dedup ----

func TestEnvelopeRequestIDPreservedAndRequired(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	// The full uint64 range is the id space; the echo must be exact.
	bigID := uint64(0xFFFFFFFFFFFFFF01)
	if resp := envCall(t, conn, bigID, "Health", nil); resp.GetCode() != envelopeCodeOK || resp.GetRequestId() != bigID {
		t.Fatalf("request_id must be preserved verbatim, got %d (code %d)", resp.GetRequestId(), resp.GetCode())
	}

	// request_id 0 is its absence: refused as an invalid request.
	envSend(t, conn, 0, serviceProtocolVersion, "Health", nil)
	resp := envRead(t, conn)
	if resp.GetCode() != envelopeCodeInvalidRequest || !strings.HasPrefix(resp.GetMessage(), errInvalidRequest) {
		t.Fatalf("a zero request_id must be code=%d, got code=%d message=%q", envelopeCodeInvalidRequest, resp.GetCode(), resp.GetMessage())
	}

	if resp := envCall(t, conn, 2, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health after id checks must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// A replayed request_id is refused with the duplicate code — completed or
// still in flight — and never executed twice.
func TestEnvelopeDuplicateRequestID(t *testing.T) {
	conn, _ := serveEnvelopeContextOverPipe(t, context.Background())
	mustHandshake(t, conn)

	// Completed: serve id 5, then replay it.
	if resp := envCall(t, conn, 5, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("first Health must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
	if resp := envCall(t, conn, 5, "Health", nil); resp.GetCode() != envelopeCodeDuplicateRequest || !strings.HasPrefix(resp.GetMessage(), errDuplicateRequest) {
		t.Fatalf("a replayed id must be code=%d %s, got code=%d message=%q", envelopeCodeDuplicateRequest, errDuplicateRequest, resp.GetCode(), resp.GetMessage())
	}

	// In flight: two frames with the same id arrive before either is answered.
	// The second must be refused without a second execution, so the two
	// answers are exactly one OK and one duplicate (in either order).
	envSend(t, conn, 6, serviceProtocolVersion, "Health", nil)
	envSend(t, conn, 6, serviceProtocolVersion, "Health", nil)
	first, second := envRead(t, conn), envRead(t, conn)
	codes := map[int32]int{first.GetCode(): 0, second.GetCode(): 0}
	codes[first.GetCode()]++
	codes[second.GetCode()]++
	if codes[envelopeCodeOK] != 1 || codes[envelopeCodeDuplicateRequest] != 1 {
		t.Fatalf("in-flight duplicate must yield exactly one OK and one duplicate, got %d and %d (ids %d,%d)", first.GetCode(), second.GetCode(), first.GetRequestId(), second.GetRequestId())
	}

	// A fresh id is still served afterwards.
	if resp := envCall(t, conn, 7, "Health", nil); resp.GetCode() != envelopeCodeOK {
		t.Fatalf("Health with a fresh id must succeed, got code=%d message=%q", resp.GetCode(), resp.GetMessage())
	}
}

// ---- shutdown semantics ----

// Shutdown must not start new handlers: a request already read but still
// waiting for a global handler slot is refused with the service-unavailable
// code when shutdown begins, and never dispatched — even after a slot frees
// up.
func TestEnvelopeShutdownRefusesPendingHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, finished := serveEnvelopeContextOverPipe(t, ctx)
	mustHandshake(t, conn)

	// Saturate the global handler slots so the next request parks in the
	// spawn path instead of executing.
	for i := 0; i < serviceHandlerConcurrencyGlobal; i++ {
		serviceHandlerSlots <- struct{}{}
	}
	envSend(t, conn, 40, serviceProtocolVersion, "Health", nil)
	time.Sleep(200 * time.Millisecond) // let the serve loop reach the slot wait

	// Begin shutdown while the request is pending.
	cancel()

	// The pending request is refused (code 7) and then the connection closes.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := envRead2(conn); err == nil {
		if resp.GetCode() != envelopeCodeServiceUnavailable {
			t.Fatalf("a pending request must be refused with code=%d, got code=%d message=%q", envelopeCodeServiceUnavailable, resp.GetCode(), resp.GetMessage())
		}
	}

	// A freed slot must not resurrect the pending request.
	<-serviceHandlerSlots
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the serve loop must finish after shutdown refused its pending request")
	}
	if currentBox() != nil {
		t.Fatal("shutdown must not have executed the pending request")
	}

	// Drain the rest of the saturation tokens.
	for len(serviceHandlerSlots) > 0 {
		<-serviceHandlerSlots
	}
}

// Shutdown must also abort a request whose payload budget is still pending:
// the reader's budget wait is cancellable, so nothing new is read or executed
// after shutdown begins.
func TestEnvelopeShutdownRefusesPendingPayloadAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, finished := serveEnvelopeContextOverPipe(t, ctx)
	mustHandshake(t, conn)

	// Exhaust the payload budget so the next frame parks inside the reader's
	// budget acquisition. Only the 4-byte length header is written: the reader
	// acquires the budget before reading the body, and on net.Pipe a full
	// frame write could not complete while nobody reads it.
	if err := servicePayloadSlots.Acquire(context.Background(), servicePayloadBudget); err != nil {
		t.Fatalf("could not saturate the payload budget: %v", err)
	}
	defer servicePayloadSlots.Release(servicePayloadBudget)

	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], 64)
	envWrite(t, conn, header[:])
	time.Sleep(200 * time.Millisecond) // let the serve loop reach the budget wait

	cancel()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the serve loop must finish when shutdown aborts a pending budget wait")
	}
	if currentBox() != nil {
		t.Fatal("shutdown must not have executed the budget-blocked request")
	}
}
