//go:build windows

// PC-110 versioned typed IPC contract for the service pipe. The PC-100
// spike's ad-hoc framing ([reqId][methodLen][method][payloadLen][payload]
// with string error statuses) is replaced here: a service frame is now
// [u32 frameLen][RequestEnvelope], the answer is [u32 frameLen]
// [ResponseEnvelope], and every request is validated as a whole — protocol
// version, request id, deadline, expected config-policy revision, operation
// registry membership, typed payload — BEFORE anything dispatches. The legacy
// GUI-child framing in dispatch.go (runDispatch) is untouched: envelopes are
// service-path-only, and this file deliberately holds no reference to the
// legacy handlers map.

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
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

// EnvelopeCode values carried in ResponseEnvelope.code. Mirrored verbatim in
// the gen/libcore.proto RequestEnvelope comment — keep the two in sync.
const (
	envelopeCodeOK                  int32 = 0
	envelopeCodeIncompatibleVersion int32 = 1
	envelopeCodeUnauthorizedClient  int32 = 2 // reserved: identity refusal happens at accept time, before any envelope is read
	envelopeCodeInvalidRequest      int32 = 3
	envelopeCodeFrameTooLarge       int32 = 4
	envelopeCodeDeadlineExceeded    int32 = 5
	envelopeCodeStalePolicyRevision int32 = 6
	envelopeCodeServiceUnavailable  int32 = 7
	envelopeCodeRuntimeFailure      int32 = 8
	envelopeCodeDuplicateRequest    int32 = 9
)

// serviceMaxEnvelopeLen bounds one request frame (envelope and its
// typed_payload together) and is checked against the declared frame length
// BEFORE any buffer is allocated. It subsumes the PC-100 spike's separate
// payload-length bound: the payload rides inside the envelope.
const serviceMaxEnvelopeLen = 32 << 20

// serviceRequestIDMemory bounds the per-connection duplicate-request window.
const serviceRequestIDMemory = 4096

// envelopeProtocolError is a framing/validation failure that is answered with
// a typed ResponseEnvelope instead of a silent close. closeConn marks frames
// that leave the stream unparsable (an oversized frame whose body was never
// read), so the connection must be closed after the typed answer.
type envelopeProtocolError struct {
	code      int32
	msg       string
	closeConn bool
}

func (e *envelopeProtocolError) Error() string { return e.msg }

// serviceEnvelopeFrame is one request frame: the decoded envelope (whose
// TypedPayload aliases buffer) and the payload-budget weight held for it.
type serviceEnvelopeFrame struct {
	env    *gen.RequestEnvelope
	buffer []byte
	weight int64
}

// releaseServiceEnvelope returns the frame's payload-budget weight. Safe to
// call more than once (the weight is zeroed on the first release).
func releaseServiceEnvelope(frame *serviceEnvelopeFrame) {
	if frame.weight > 0 {
		servicePayloadSlots.Release(frame.weight)
		frame.weight = 0
	}
}

// readServiceEnvelope reads one length-prefixed RequestEnvelope frame. The
// declared length is checked against serviceMaxEnvelopeLen before the buffer
// is allocated, and the payload budget is acquired for the frame size so the
// aggregate in-flight envelope memory stays bounded. allowIdle lets an
// established client idle between frames: the first length byte blocks
// indefinitely, the rest of the frame must arrive within serviceReadTimeout.
// I/O errors and envelopeProtocolError are distinguished by errors.As.
func readServiceEnvelope(ctx context.Context, conn net.Conn, allowIdle bool) (serviceEnvelopeFrame, error) {
	var frame serviceEnvelopeFrame
	if !allowIdle {
		if err := conn.SetReadDeadline(time.Now().Add(serviceReadTimeout)); err != nil {
			return frame, err
		}
	}
	var lenBytes [4]byte
	if allowIdle {
		if _, err := io.ReadFull(conn, lenBytes[:1]); err != nil {
			return frame, err
		}
		if err := conn.SetReadDeadline(time.Now().Add(serviceReadTimeout)); err != nil {
			return frame, err
		}
		if _, err := io.ReadFull(conn, lenBytes[1:]); err != nil {
			return frame, err
		}
	} else if _, err := io.ReadFull(conn, lenBytes[:]); err != nil {
		return frame, err
	}
	defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck -- a later I/O reports connection errors

	frameLen := binary.LittleEndian.Uint32(lenBytes[:])
	if frameLen == 0 {
		// Four bytes consumed, no body: the stream stays parsable.
		return frame, &envelopeProtocolError{code: envelopeCodeInvalidRequest, msg: fmt.Sprintf("%s: empty frame", errInvalidRequest)}
	}
	if frameLen > serviceMaxEnvelopeLen {
		// Refused before any allocation: only the 4-byte header was read, so
		// the body (if any) would desynchronize the stream — close after the
		// typed answer.
		return frame, &envelopeProtocolError{
			code:      envelopeCodeFrameTooLarge,
			msg:       fmt.Sprintf("%s: envelope frame %d exceeds the %d byte limit", errFrameTooLarge, frameLen, serviceMaxEnvelopeLen),
			closeConn: true,
		}
	}
	if err := servicePayloadSlots.Acquire(ctx, int64(frameLen)); err != nil {
		return frame, err // shutdown (or budget exhaustion with ctx done); nothing to answer
	}
	frame.weight = int64(frameLen)
	frame.buffer = make([]byte, frameLen)
	if _, err := io.ReadFull(conn, frame.buffer); err != nil {
		releaseServiceEnvelope(&frame)
		return frame, err
	}
	env := &gen.RequestEnvelope{}
	if err := proto.Unmarshal(frame.buffer, env); err != nil {
		// The whole frame was consumed, so the stream stays parsable and the
		// connection can serve the next frame.
		releaseServiceEnvelope(&frame)
		return frame, &envelopeProtocolError{code: envelopeCodeInvalidRequest, msg: fmt.Sprintf("%s: malformed request envelope: %v", errInvalidRequest, err)}
	}
	frame.env = env
	return frame, nil
}

// writeServiceEnvelopeResponse frames and writes one ResponseEnvelope:
// [u32 frameLen][envelope bytes]. mu is optional (nil where the caller is
// already the only writer on the connection).
func writeServiceEnvelopeResponse(mu *sync.Mutex, conn net.Conn, resp *gen.ResponseEnvelope) error {
	body, err := proto.Marshal(resp)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if err := conn.SetWriteDeadline(time.Now().Add(serviceWriteTimeout)); err != nil {
		return err
	}
	defer conn.SetWriteDeadline(time.Time{}) //nolint:errcheck -- a later I/O reports connection errors
	_, err = conn.Write(frame)
	return err
}

// serviceOperationDef is one registry entry: the only way a service-mode
// operation name becomes an executable handler. It replaces the PC-100
// spike's serviceMethodAllowlist so exactly ONE table decides what a service
// client may call — and it deliberately does not fall back to the legacy
// handlers map, which carries the privileged utility RPC and the raw Start.
type serviceOperationDef struct {
	name   string
	decode func([]byte) (proto.Message, error)
	call   func(context.Context, proto.Message) (proto.Message, error)
}

// typedServiceOperation binds a typed handler into the registry: the payload
// must decode into exactly the operation's request type, and a payload that
// decodes into something else is a typed invalid request, never a crash.
// Decoding is strict: protobuf would silently park fields of another message
// type (or a newer client's fields) in unknown fields, so any unknown-field
// residue rejects the payload — an operation only accepts the fields it
// declares.
func typedServiceOperation[Req any, PReq interface {
	*Req
	proto.Message
}, Resp proto.Message](name string, fn func(context.Context, PReq) (Resp, error)) serviceOperationDef {
	return serviceOperationDef{
		name: name,
		decode: func(payload []byte) (proto.Message, error) {
			req := PReq(new(Req))
			if err := proto.Unmarshal(payload, req); err != nil {
				return nil, err
			}
			if unknown := req.ProtoReflect().GetUnknown(); len(unknown) > 0 {
				return nil, fmt.Errorf("payload carries %d bytes of fields unknown to %s", len(unknown), name)
			}
			return req, nil
		},
		call: func(ctx context.Context, msg proto.Message) (proto.Message, error) {
			return fn(ctx, msg.(PReq))
		},
	}
}

// serviceOperations is the ONLY operation table the service pipe serves:
// Hello (the mandatory handshake), Health, CheckConfig, Start, Stop. The
// privileged legacy RPC (SetSystemDNS, InstallDashboard, CloseConnections,
// QueryStats, GenWgKeyPair, WarpRegister, ...) is absent by construction, and
// CheckConfig/Start bind to the Service* handlers that enforce the config
// filesystem policy — never to the raw legacy ones.
var serviceOperations = map[string]serviceOperationDef{
	"Hello":       typedServiceOperation("Hello", globalServer.Hello),
	"Health":      typedServiceOperation("Health", globalServer.Health),
	"CheckConfig": typedServiceOperation("CheckConfig", globalServer.ServiceCheckConfig),
	"Start":       typedServiceOperation("Start", globalServer.ServiceStart),
	"Stop":        typedServiceOperation("Stop", globalServer.Stop),
}

// serviceRequestIDs remembers the request ids already executed on one
// connection so a replayed id gets the typed duplicate error instead of a
// second execution. Ids are recorded only when a request is accepted for
// execution, so a client can retry a rejected request with the same id.
// Memory is bounded: when the window is full, the oldest id is forgotten —
// a replay older than the window is then the client's own contract violation.
type serviceRequestIDs struct {
	mu    sync.Mutex
	seen  map[uint64]struct{}
	order []uint64
}

func newServiceRequestIDs() *serviceRequestIDs {
	return &serviceRequestIDs{seen: make(map[uint64]struct{}, serviceRequestIDMemory)}
}

func (s *serviceRequestIDs) tryAdd(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[id]; dup {
		return false
	}
	s.seen[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > serviceRequestIDMemory {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// envelopeContext derives the handler context: bound by the client deadline
// when one was sent, otherwise bounded only by the connection's lifetime
// context (shutdown). Deadlines that have already passed are rejected by the
// serve loop before dispatch, so this never sees an expired deadline.
func envelopeContext(base context.Context, env *gen.RequestEnvelope) (context.Context, context.CancelFunc) {
	if ms := env.GetDeadlineUnixMs(); ms > 0 {
		return context.WithDeadline(base, time.UnixMilli(ms))
	}
	return context.WithCancel(base)
}

// envelopeCodeForHandlerError classifies a handler outcome into the envelope
// code space. Deliberate typed refusals (the ERR_* prefixes the service
// handlers return for invalid requests and config-policy violations) are
// "invalid request" at the envelope level; a handler error that is the
// runtime failing maps to "runtime failure"; a deadline hit inside the
// handler maps to "deadline exceeded". The specific typed prefix always
// travels in the message.
func envelopeCodeForHandlerError(err error) int32 {
	if errors.Is(err, context.DeadlineExceeded) {
		return envelopeCodeDeadlineExceeded
	}
	msg := err.Error()
	for _, prefix := range []string{
		errInvalidRequest,
		errConfigPolicy,
		errMethodNotAllowed,
		errNoHandshake,
		errProtocolVersion,
	} {
		if strings.HasPrefix(msg, prefix) {
			return envelopeCodeInvalidRequest
		}
	}
	return envelopeCodeRuntimeFailure
}
