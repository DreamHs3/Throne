//go:build windows

// PC-100 service-capable runtime spike (ADR-002 variant A): the same
// ThroneCore binary gains an explicit "service" entry point. Service mode
// LISTENS on a named pipe with an explicit SDDL (replacing the child mode's
// parentcheck at the pipe boundary), requires a versioned handshake before
// any other method, serves the existing handlers, and survives client
// disconnects — the legacy child mode and its checks are untouched.

package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/xray"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	runtimeDebug "runtime/debug"
	"strings"
	"sync"
	"time"
	"unsafe"

	C "github.com/sagernet/sing-box/constant"
	"github.com/tailscale/go-winio"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

const (
	proxyCoreServiceName = "ProxyCoreService"

	// Fixed, documented pipe name — never a random one (no security through obscurity).
	defaultServicePipeName = `\\.\pipe\ProxyCoreService`
	// Deny-by-default DACL: only SYSTEM and Administrators may connect.
	// The per-user SID grant arrives with the installer (PC-120) via
	// THRONE_SERVICE_SDDL; a fixed name plus a restrictive ACL keeps the
	// spike honest without pretending a full SID validation exists (PC-110).
	defaultServiceSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

	// Per-connection and aggregate bounds for the service path. The frame and
	// payload limit contract (PC-110) lives in service_envelope_windows.go:
	// serviceMaxEnvelopeLen bounds one request frame and is checked before
	// any allocation; servicePayloadBudget bounds the aggregate envelope
	// memory held across the service.
	serviceMaxConnections = 16
	servicePayloadBudget  = 64 << 20

	// Handler concurrency is bounded both per connection and across the
	// service, so opening many authenticated connections cannot multiply the
	// privileged runtime's work without limit.
	serviceHandlerConcurrencyPerConn = 8
	serviceHandlerConcurrencyGlobal  = 16
)

var serviceReadTimeout = 30 * time.Second
var serviceWriteTimeout = 30 * time.Second

// The service pipe protocol version, carried in every RequestEnvelope and
// checked before anything dispatches. Version 1 is the PC-110 typed envelope
// protocol; the pre-envelope PC-100 spike framing never shipped to a client.
// Incompatible clients get the typed version error and are disconnected.
const serviceProtocolVersion = 1

// Machine-readable error prefixes carried in ResponseEnvelope.message. The
// envelope code (service_envelope_windows.go) classifies the failure for the
// wire; the prefix names the exact contract a client can grep for.
const (
	errNoHandshake         = "ERR_NO_HANDSHAKE"
	errProtocolVersion     = "ERR_PROTOCOL_VERSION"
	errInvalidRequest      = "ERR_INVALID_REQUEST"
	errMethodNotAllowed    = "ERR_METHOD_NOT_ALLOWED"
	errFrameTooLarge       = "ERR_FRAME_TOO_LARGE"
	errDeadlineExceeded    = "ERR_DEADLINE_EXCEEDED"
	errStalePolicyRevision = "ERR_STALE_POLICY_REVISION"
	errDuplicateRequest    = "ERR_DUPLICATE_REQUEST"
)

// The service method table is serviceOperations (service_envelope_windows.go,
// PC-110): the single registry that maps an envelope operation to a typed
// handler. It carries exactly Hello, Health, CheckConfig, Start and Stop and
// deliberately does NOT fall back to the legacy handlers map — that table
// carries privileged utility RPC (SetSystemDNS, InstallDashboard,
// CloseConnections, ...) and, via raw Start, the extra-process execution
// surface, none of which a service client may drive. The PC-100 spike's
// separate serviceMethodAllowlist was folded into the registry so exactly one
// table decides what a service client may call.

func servicePipeName() string {
	if v := os.Getenv("THRONE_SERVICE_PIPE"); v != "" {
		return v
	}
	return defaultServicePipeName
}

func serviceSDDL() (string, error) {
	if v := os.Getenv("THRONE_SERVICE_SDDL"); v != "" {
		return safeServiceSDDL(v)
	}
	return defaultServiceSDDL, nil
}

// broadSDDLTrusteeSIDs are the raw-SID spellings of the broad trustees the
// guard already rejects by abbreviation. The trustee field is matched by the
// security subsystem as a plain string, so D:P(A;;GA;;;S-1-1-0) names
// Everyone exactly like D:P(A;;GA;;;WD) does — only the WD/AN/AU/BU literals
// were checked before round 3.
var broadSDDLTrusteeSIDs = map[string]string{
	"S-1-1-0":      "Everyone",
	"S-1-5-7":      "Anonymous",
	"S-1-5-11":     "Authenticated Users",
	"S-1-5-32-545": "Builtin Users",
	"S-1-5-32-546": "Builtin Guests",
}

// safeServiceSDDL protects the SDDL override from unsafe substitution. A
// usable DACL must be protected ("D:P" - inherited ACEs off) and must not
// grant access to broad trustees: Everyone (WD), Anonymous (AN),
// Authenticated Users (AU) or Builtin Users (BU) — by abbreviation or by raw
// SID (Everyone S-1-1-0, Anonymous S-1-5-7, Authenticated Users S-1-5-11,
// Builtin Users S-1-5-32-545, Builtin Guests S-1-5-32-546). Per-user SID
// grants ("A;;GA;;;S-1-5-21-...") and SYSTEM/ADMINISTRATORS remain fine. An
// unsafe override fails the listener start - never a silent fallback to
// something broader.
func safeServiceSDDL(sddl string) (string, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return "", fmt.Errorf("%s: invalid service SDDL: %v", errInvalidRequest, err)
	}
	canonical := sd.String()
	if !strings.HasPrefix(canonical, "D:P") {
		return "", fmt.Errorf("%s: service SDDL must start with a protected DACL (D:P)", errInvalidRequest)
	}
	rest := strings.TrimPrefix(canonical, "D:P")
	if rest == "" {
		return "", fmt.Errorf("%s: service SDDL must contain an access-control entry", errInvalidRequest)
	}
	for rest != "" {
		if rest[0] != '(' {
			return "", fmt.Errorf("%s: unsupported service SDDL section", errInvalidRequest)
		}
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return "", fmt.Errorf("%s: malformed service SDDL ACE", errInvalidRequest)
		}
		fields := strings.Split(rest[1:end], ";")
		if len(fields) != 6 {
			return "", fmt.Errorf("%s: unsupported service SDDL ACE", errInvalidRequest)
		}
		aceType := strings.ToUpper(fields[0])
		if aceType != "A" && aceType != "D" {
			return "", fmt.Errorf("%s: unsupported service SDDL ACE type %q", errInvalidRequest, fields[0])
		}
		if aceType == "A" {
			trustee := strings.ToUpper(fields[5])
			if label, broad := broadSDDLTrusteeSIDs[trustee]; broad {
				return "", fmt.Errorf("%s: service SDDL must not grant access to %s (%s)", errInvalidRequest, label, trustee)
			}
			for _, broad := range []string{"WD", "AN", "AU", "BU", "BG"} {
				if trustee == broad {
					return "", fmt.Errorf("%s: service SDDL must not grant access to %s", errInvalidRequest, trustee)
				}
			}
		}
		rest = rest[end+1:]
	}
	return canonical, nil
}

// sddlACETrustees returns the trustee field of every allow ACE in the SDDL
// string. An ACE body is type;flags;rights;objectGUID;inheritGUID;trustee —
// the "(A;" split consumes the type and its delimiter, so the trustee is the
// last remaining field.
func sddlACETrustees(sddl string) []string {
	var trustees []string
	for _, ace := range strings.Split(sddl, "(A;") {
		fields := strings.Split(strings.TrimSuffix(ace, ")"), ";")
		if len(fields) > 0 {
			trustees = append(trustees, fields[len(fields)-1])
		}
	}
	return trustees
}

// sddlGrantsTrustee reports whether any allow ACE in the SDDL string grants
// access to the given trustee abbreviation. Deliberately simple: exact match
// on the trustee field of each allow ACE.
func sddlGrantsTrustee(sddl string, trustee string) bool {
	for _, t := range sddlACETrustees(sddl) {
		if t == trustee {
			return true
		}
	}
	return false
}

// applyServiceModeSettings pins spike-level runtime settings. The GUI child
// mode prints full core configs when THRONE_CORE_DEBUG=1 (documented upstream
// debt); service mode never does.
func applyServiceModeSettings() {
	debug = false
}

func listenServicePipe() (net.Listener, error) {
	sddl, err := serviceSDDL()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(servicePipeName(), &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	})
}

func runServiceMode() error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("cannot determine service context: %w", err)
	}
	if !isSvc {
		return errors.New("service mode requested outside the Windows SCM")
	}
	return svc.Run(proxyCoreServiceName, &proxyCoreServiceHandler{})
}

// connSet tracks open client connections so a service shutdown can unblock
// the serve loops deterministically instead of waiting on stalled readers.
type connSet struct {
	mu sync.Mutex
	m  map[net.Conn]struct{}
}

func newConnSet() *connSet { return &connSet{m: make(map[net.Conn]struct{})} }

func (cs *connSet) tryAdd(c net.Conn) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.m) >= serviceMaxConnections {
		return false
	}
	cs.m[c] = struct{}{}
	return true
}

func (cs *connSet) remove(c net.Conn) {
	cs.mu.Lock()
	delete(cs.m, c)
	cs.mu.Unlock()
}

func (cs *connSet) closeAll() {
	cs.mu.Lock()
	for c := range cs.m {
		_ = c.Close()
	}
	cs.mu.Unlock()
}

// serviceHandlersWG tracks in-flight handler goroutines across all
// connections so shutdown can wait for them in a bounded way.
var serviceHandlersWG sync.WaitGroup
var serviceHandlerSlots = make(chan struct{}, serviceHandlerConcurrencyGlobal)
var servicePayloadSlots = semaphore.NewWeighted(servicePayloadBudget)

// waitServiceHandlers waits up to d for in-flight handlers to finish.
func waitServiceHandlers(d time.Duration) {
	done := make(chan struct{})
	go func() {
		serviceHandlersWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// serveServiceListener is the accept loop. Every client is authorized by
// token identity before it can reach the handshake, let alone a method.
func serveServiceListener(listener net.Listener, conns *connSet, stopCh <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-stopCh:
				return
			default:
				log.Printf("service pipe accept error: %v", err)
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		if !authorizeServiceClient(conn) {
			_ = conn.Close()
			continue
		}
		if !conns.tryAdd(conn) {
			_ = conn.Close()
			continue
		}
		go func() {
			defer conns.remove(conn)
			serveServiceConnContext(ctx, conn)
		}()
	}
}

// builtinAdministratorsSID is the Windows built-in Administrators group;
// ADR-001 admits it alongside the installing owner's SID.
const builtinAdministratorsSID = "S-1-5-32-544"

// authorizeServiceClient is a hook so tests can drive the accept loop with
// stub identities; production always uses authorizeServiceClientReal.
var authorizeServiceClient = authorizeServiceClientReal

// authorizeServiceClientReal resolves the connecting process's token and
// applies the ADR-001 identity policy: the configured owner SID(s) and
// Administrators. Unknown or unresolvable identities are denied (fail closed)
// and only the SID - never any payload - is logged.
func authorizeServiceClientReal(conn net.Conn) bool {
	sid, groups, ok := clientTokenIdentity(conn)
	if !ok {
		log.Printf("service client rejected: client identity unavailable")
		return false
	}
	if serviceClientAllowed(sid, groups) {
		return true
	}
	log.Printf("service client rejected: sid %s is not in the allowed set", sid)
	return false
}

// serviceClientAllowed is the pure ADR-001 identity policy.
func serviceClientAllowed(sid string, groups []string) bool {
	for _, group := range groups {
		if group == builtinAdministratorsSID {
			return true
		}
	}
	for _, allowed := range allowedServiceSIDs() {
		if sid != "" && sid == allowed {
			return true
		}
	}
	return false
}

// allowedServiceSIDs lists the installing owner's SID(s), configured via
// THRONE_SERVICE_ALLOWED_SIDS (comma separated). The installer (PC-120) owns
// this setting; with it unset only Administrators are admitted.
func allowedServiceSIDs() []string {
	var out []string
	for _, item := range strings.Split(os.Getenv("THRONE_SERVICE_ALLOWED_SIDS"), ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// clientTokenIdentity resolves the pipe client's user SID and group SIDs.
// The identity is bound to the connection's client process at accept time;
// a client spawning other processes later changes nothing about this check.
func clientTokenIdentity(conn net.Conn) (string, []string, bool) {
	fdc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return "", nil, false
	}
	var clientPID uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fdc.Fd()), &clientPID); err != nil {
		return "", nil, false
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, clientPID)
	if err != nil {
		return "", nil, false
	}
	defer windows.CloseHandle(proc)
	var token windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &token); err != nil {
		return "", nil, false
	}
	defer token.Close()

	tokenUser, err := tokenInformation(token, windows.TokenUser)
	if err != nil {
		return "", nil, false
	}
	sid := (*windows.Tokenuser)(unsafe.Pointer(&tokenUser[0])).User.Sid.String()

	var groups []string
	if tokenGroupsBuf, err := tokenInformation(token, windows.TokenGroups); err == nil {
		groups = tokenGroupSIDs(tokenGroupsBuf)
	}
	return sid, groups, true
}

// tokenGroupSIDs decodes a TokenGroups buffer into group SID strings. The
// Groups field is a fixed-size [1]SIDAndAttributes array whose real length is
// GroupCount, so the members are re-sliced unsafely — indexing the array
// directly panics for any token with more than one group.
func tokenGroupSIDs(buf []byte) []string {
	if len(buf) < 4 {
		return nil
	}
	info := (*windows.Tokengroups)(unsafe.Pointer(&buf[0]))
	if info.GroupCount == 0 {
		return nil
	}
	members := unsafe.Slice((*windows.SIDAndAttributes)(unsafe.Pointer(&info.Groups[0])), info.GroupCount)
	var out []string
	for _, member := range members {
		if !tokenGroupEnabled(member.Attributes) {
			continue
		}
		out = append(out, member.Sid.String())
	}
	return out
}

func tokenGroupEnabled(attributes uint32) bool {
	return attributes&windows.SE_GROUP_ENABLED != 0 && attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0
}

// tokenInformation queries a token information class into a fresh buffer.
func tokenInformation(token windows.Token, class uint32) ([]byte, error) {
	var needed uint32
	if err := windows.GetTokenInformation(token, class, nil, 0, &needed); err != nil && needed == 0 {
		return nil, err
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(token, class, &buf[0], uint32(needed), &needed); err != nil {
		return nil, err
	}
	return buf, nil
}

type proxyCoreServiceHandler struct{}

// Execute implements svc.Handler. Testable without SCM: feed ChangeRequests
// and collect Status values directly.
func (h *proxyCoreServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending, WaitHint: 15000}

	applyServiceModeSettings()

	listener, err := listenServicePipe()
	if err != nil {
		log.Printf("service pipe listen failed: %v", err)
		status <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	stopCh := make(chan struct{})
	var stopOnce sync.Once
	conns := newConnSet()

	go serveServiceListener(listener, conns, stopCh)

	status <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		c := <-r
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending, WaitHint: 10000}
			stopOnce.Do(func() { close(stopCh) })
			_ = listener.Close()
			conns.closeAll()
			// Stop is idempotent by upstream contract (no instance → nil error).
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
			// Give in-flight handlers a bounded moment, then report stopped.
			waitServiceHandlers(2 * time.Second)
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		default:
			log.Printf("unexpected service control request #%d", c)
		}
	}
}

// The service wire framing (PC-110) lives in service_envelope_windows.go:
// readServiceEnvelope parses [u32 frameLen][RequestEnvelope] and
// writeServiceEnvelopeResponse writes [u32 frameLen][ResponseEnvelope]. The
// legacy GUI-child frame layout stays in dispatch.go only — the two protocols
// share no code, so neither can drift into the other.

// serveServiceConn handles one client connection with the PC-110 envelope
// protocol. A disconnect is a normal service-mode event: unlike runDispatch,
// it never terminates the process.
//
// Every request is validated as a whole before anything dispatches:
// protocol version → shutdown gate → handshake state → request id → deadline
// → expected config-policy revision → operation registry → typed payload.
// Requests the serve loop itself refuses (unknown operation, wrong payload,
// stale revision, expired deadline, duplicate id) never reach a handler and
// never break the connection, so the client can correct and continue.
func serveServiceConn(conn net.Conn) {
	serveServiceConnContext(context.Background(), conn)
}

func serveServiceConnContext(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	var writeMu sync.Mutex
	respond := func(reqID uint64, code int32, msg string, payload []byte) error {
		return writeServiceEnvelopeResponse(&writeMu, conn, &gen.ResponseEnvelope{
			RequestId:              To(reqID),
			Code:                   To(code),
			Message:                To(msg),
			TypedPayload:           payload,
			ServiceProtocolVersion: To(int32(serviceProtocolVersion)),
		})
	}
	// answer releases the frame and replies with a serve-loop refusal; it
	// reports whether the connection is still usable for the next frame.
	answer := func(frame *serviceEnvelopeFrame, reqID uint64, code int32, msg string) bool {
		releaseServiceEnvelope(frame)
		return respond(reqID, code, msg, nil) == nil
	}

	ids := newServiceRequestIDs()

	// Mandatory versioned handshake: the first envelope must be Hello and
	// carry the protocol version the service speaks.
	first, err := readServiceEnvelope(ctx, conn, false)
	if err != nil {
		var protoErr *envelopeProtocolError
		if errors.As(err, &protoErr) {
			_ = respond(0, protoErr.code, protoErr.msg, nil)
		}
		return
	}
	defer releaseServiceEnvelope(&first)
	env := first.env
	if env.GetProtocolVersion() != serviceProtocolVersion {
		// An incompatible client is told why and disconnected.
		_ = respond(env.GetRequestId(), envelopeCodeIncompatibleVersion,
			fmt.Sprintf("%s: client %d, service %d", errProtocolVersion, env.GetProtocolVersion(), serviceProtocolVersion), nil)
		return
	}
	if env.GetOperation() != "Hello" {
		answer(&first, env.GetRequestId(), envelopeCodeInvalidRequest,
			fmt.Sprintf("%s: first request must be the Hello operation", errNoHandshake))
		return
	}
	opHello := serviceOperations["Hello"]
	if _, decodeErr := opHello.decode(env.GetTypedPayload()); decodeErr != nil {
		answer(&first, env.GetRequestId(), envelopeCodeInvalidRequest,
			fmt.Sprintf("%s: Hello payload: %v", errInvalidRequest, decodeErr))
		return
	}
	hsResp, err := proto.Marshal(&gen.HandshakeResp{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		ServiceVersion:  To(C.Version),
	})
	if err != nil {
		return
	}
	if err := respond(env.GetRequestId(), envelopeCodeOK, "", hsResp); err != nil {
		return
	}
	releaseServiceEnvelope(&first)

	handlerSem := make(chan struct{}, serviceHandlerConcurrencyPerConn)
	for {
		frame, err := readServiceEnvelope(ctx, conn, true)
		if err != nil {
			var protoErr *envelopeProtocolError
			if errors.As(err, &protoErr) {
				_ = respond(0, protoErr.code, protoErr.msg, nil)
				if !protoErr.closeConn {
					continue
				}
			}
			// Client went away, sent an unparsable stream, or the service is
			// shutting down: close and move on.
			return
		}
		env := frame.env
		reqID := env.GetRequestId()

		if env.GetProtocolVersion() != serviceProtocolVersion {
			// A client that suddenly speaks another version is incompatible;
			// answer and disconnect.
			answer(&frame, reqID, envelopeCodeIncompatibleVersion,
				fmt.Sprintf("%s: client %d, service %d", errProtocolVersion, env.GetProtocolVersion(), serviceProtocolVersion))
			return
		}

		// Shutdown refuses new work: nothing that arrives from here on
		// reaches a handler.
		if ctx.Err() != nil {
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}

		if env.GetOperation() == "Hello" {
			if !answer(&frame, reqID, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: handshake already complete", errNoHandshake)) {
				return
			}
			continue
		}

		if reqID == 0 {
			// request_id is the dedup and correlation key; 0 is its absence.
			if !answer(&frame, 0, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: request_id is required", errInvalidRequest)) {
				return
			}
			continue
		}

		if ms := env.GetDeadlineUnixMs(); ms > 0 && !time.Now().Before(time.UnixMilli(ms)) {
			if !answer(&frame, reqID, envelopeCodeDeadlineExceeded,
				fmt.Sprintf("%s: deadline %d already passed", errDeadlineExceeded, ms)) {
				return
			}
			continue
		}

		if rev := env.GetExpectedPolicyRevision(); rev != 0 && rev != configPolicyRevision {
			if !answer(&frame, reqID, envelopeCodeStalePolicyRevision,
				fmt.Sprintf("%s: client expects config policy revision %d, service runs %d", errStalePolicyRevision, rev, configPolicyRevision)) {
				return
			}
			continue
		}

		op, found := serviceOperations[env.GetOperation()]
		if !found {
			// Everything outside the registry — known to the legacy table or
			// not — gets the same stable typed refusal, and the connection
			// stays usable.
			if !answer(&frame, reqID, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: %q is not available in service mode", errMethodNotAllowed, env.GetOperation())) {
				return
			}
			continue
		}

		reqMsg, decodeErr := op.decode(env.GetTypedPayload())
		if decodeErr != nil {
			if !answer(&frame, reqID, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: %s payload: %v", errInvalidRequest, op.name, decodeErr)) {
				return
			}
			continue
		}

		// The id is recorded only when the request is accepted for execution,
		// so a client can retry a refused request with the same id.
		if !ids.tryAdd(reqID) {
			if !answer(&frame, reqID, envelopeCodeDuplicateRequest,
				fmt.Sprintf("%s: request id %d was already executed on this connection", errDuplicateRequest, reqID)) {
				return
			}
			continue
		}

		// Handler concurrency is bounded both per connection and across the
		// service; both waits abort on shutdown without spawning a handler.
		serviceHandlersWG.Add(1)
		select {
		case serviceHandlerSlots <- struct{}{}:
		case <-ctx.Done():
			serviceHandlersWG.Done()
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}
		select {
		case handlerSem <- struct{}{}:
		case <-ctx.Done():
			<-serviceHandlerSlots
			serviceHandlersWG.Done()
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}

		// The payload budget travels with the handler so the aggregate
		// in-flight envelope memory stays bounded for the handler's lifetime.
		weight := frame.weight
		frame.weight = 0
		hctx, cancel := envelopeContext(ctx, env)
		go func(requestID uint64, op serviceOperationDef, msg proto.Message) {
			defer serviceHandlersWG.Done()
			defer cancel()
			defer func() { <-handlerSem }()
			defer func() { <-serviceHandlerSlots }()
			defer func() { servicePayloadSlots.Release(weight) }()
			defer func() {
				if r := recover(); r != nil {
					// Same stack parity as runDispatch: a panic must be diagnosable.
					log.Printf("panic in %s: %v\n%s", op.name, r, runtimeDebug.Stack())
					_ = respond(requestID, envelopeCodeRuntimeFailure,
						fmt.Sprintf("core panic in %s: %v", op.name, r), nil)
				}
			}()
			if ctx.Err() != nil {
				// Shutdown won the race with the spawn; run nothing.
				_ = respond(requestID, envelopeCodeServiceUnavailable, "service is shutting down", nil)
				return
			}
			respMsg, callErr := op.call(hctx, msg)
			if callErr != nil {
				_ = respond(requestID, envelopeCodeForHandlerError(callErr), callErr.Error(), nil)
				return
			}
			respBytes, marshalErr := proto.Marshal(respMsg)
			if marshalErr != nil {
				_ = respond(requestID, envelopeCodeRuntimeFailure,
					fmt.Sprintf("%s: %s response: %v", errInvalidRequest, op.name, marshalErr), nil)
				return
			}
			_ = respond(requestID, envelopeCodeOK, "", respBytes)
		}(reqID, op, reqMsg)
	}
}

// Hello and Health are the service-mode additive methods. They are NOT part
// of the proto LibcoreService service block, so the generated server
// interface (and the non-Windows builds) are unaffected; on Windows they are
// registered into the existing handlers table below.

func (s *server) Hello(ctx context.Context, in *gen.HandshakeReq) (*gen.HandshakeResp, error) {
	if in.GetProtocolVersion() != serviceProtocolVersion {
		return nil, fmt.Errorf("%s: client %d, service %d", errProtocolVersion, in.GetProtocolVersion(), serviceProtocolVersion)
	}
	return &gen.HandshakeResp{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		ServiceVersion:  To(C.Version),
	}, nil
}

func (s *server) Health(ctx context.Context, _ *gen.EmptyReq) (*gen.HealthResp, error) {
	return &gen.HealthResp{
		RuntimeRunning:  To(currentBox() != nil),
		ProtocolVersion: To(int32(serviceProtocolVersion)),
	}, nil
}

// ServiceCheckConfig is the service-mode CheckConfig. It normalizes absent
// proto2 optional fields to their defaults before delegating, because the
// upstream handlers dereference optional pointers directly (server.go:579
// `*in.CoreConfig`; also :399/:434/:474) and a client that omits a field
// would otherwise crash the handler — an upstream robustness defect recorded
// for the PC-110 typed contract, deliberately not patched in server.go.
// The same filesystem policy as Start is enforced so a config cannot be
// validated in the morning and run in the evening (what CheckConfig accepts,
// Start accepts; what Start rejects, CheckConfig rejects) — including
// xray_config and every xray_full_configs entry, which Start validated and
// CheckConfig skipped before round 3.
func (s *server) ServiceCheckConfig(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
	normalizeLoadConfigReq(in)
	coreConfig, err := applyServiceConfigPolicy(in.GetCoreConfig(), configServiceDataDir())
	if err != nil {
		return nil, err
	}
	in.CoreConfig = proto.String(coreConfig)
	if err := validateServiceXrayConfigPolicy(in.GetXrayConfig()); err != nil {
		return nil, err
	}
	for _, full := range in.GetXrayFullConfigs() {
		if err := validateServiceXrayConfigPolicy(full); err != nil {
			return nil, err
		}
		if err := xray.CheckXrayConfig(full); err != nil {
			return &gen.ErrorResp{Error: To(err.Error())}, nil
		}
	}
	return globalServer.CheckConfig(ctx, in)
}

// ServiceStart is the service-mode Start. It rejects the whole extra-process
// execution surface — arbitrary executable path, arguments and config file —
// before anything is parsed or spawned, then enforces the filesystem config
// policy (normalize cache_file/external_ui to service-owned values, reject
// every other path-bearing field), then delegates to the legacy Start.
//
// What remains outside PC-100 (documented): the typed-parameter contract
// where the service builds its own configuration end-to-end (PC-110+), the
// per-user SID grant from the installer (PC-120) and every SCM/VM test.
func (s *server) ServiceStart(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
	if in.GetNeedExtraProcess() ||
		in.GetExtraProcessPath() != "" ||
		in.GetExtraProcessArgs() != "" ||
		in.GetExtraProcessConf() != "" ||
		in.GetExtraNoOut() {
		return nil, fmt.Errorf("%s: extra process execution fields are not permitted in service mode", errInvalidRequest)
	}
	if in.GetNeedXray() && in.XrayConfig == nil {
		return nil, fmt.Errorf("%s: need_xray requires xray_config", errInvalidRequest)
	}
	normalizeLoadConfigReq(in)
	coreConfig, err := applyServiceConfigPolicy(in.GetCoreConfig(), configServiceDataDir())
	if err != nil {
		return nil, err
	}
	in.CoreConfig = proto.String(coreConfig)
	if err := validateServiceXrayConfigPolicy(in.GetXrayConfig()); err != nil {
		return nil, err
	}
	for _, full := range in.GetXrayFullConfigs() {
		if err := validateServiceXrayConfigPolicy(full); err != nil {
			return nil, err
		}
	}
	return globalServer.Start(ctx, in)
}

// normalizeLoadConfigReq materializes absent proto2 optional pointers to
// their wire defaults so the legacy handlers' direct pointer dereferences
// behave exactly as they do for the GUI, which always sends every field.
func normalizeLoadConfigReq(in *gen.LoadConfigReq) {
	if in == nil {
		return
	}
	if in.CoreConfig == nil {
		in.CoreConfig = proto.String("")
	}
	if in.NeedExtraProcess == nil {
		in.NeedExtraProcess = proto.Bool(false)
	}
	if in.ExtraNoOut == nil {
		in.ExtraNoOut = proto.Bool(false)
	}
	if in.NeedXray == nil {
		in.NeedXray = proto.Bool(false)
	}
}

func init() {
	handlers["Hello"] = handle(globalServer.Hello)
	handlers["Health"] = handle(globalServer.Health)
}
