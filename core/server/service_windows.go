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

var serviceHandlerSlots = make(chan struct{}, serviceHandlerConcurrencyGlobal)
var servicePayloadSlots = semaphore.NewWeighted(servicePayloadBudget)

// errServiceStopping is the typed refusal of work that arrived after the
// service began stopping; the envelope classifier maps it to
// envelopeCodeServiceUnavailable.
var errServiceStopping = errors.New("service is shutting down")

// serviceRuntimeMu guards the service path's runtime-creation barrier state:
// serviceRuntimeStopping (the shutdown mark, set once per run) and
// serviceRuntimeStarting (ServiceStart executions that passed the pre-check
// and have not finished their post-check yet). Every critical section under
// this lock is O(1) flag/counter work — the lock is NEVER held across the
// legacy Start, so the stop path cannot be stalled by a slow or hanging
// runtime creation (F7: the previous design held this lock across
// globalServer.Start while the stop path blocked on it with no timeout,
// which contradicted the documented bounded shutdown).
var serviceRuntimeMu sync.Mutex
var serviceRuntimeStopping bool
var serviceRuntimeStarting int

// beginServiceRuntimeShutdown raises the shutdown mark. It is O(1): only flag
// work happens under the lock, so it cannot block on an in-flight runtime
// creation. A Start that finishes past the mark tears its own runtime down in
// its post-check (see ServiceStart) instead of the stop path waiting for it.
func beginServiceRuntimeShutdown() {
	serviceRuntimeMu.Lock()
	serviceRuntimeStopping = true
	serviceRuntimeMu.Unlock()
}

// resetServiceRuntimeBarrier clears the barrier state for a fresh service
// run. Production exits after Stopped so this is belt-and-braces (and it lets
// tests drive Execute repeatedly in one process without cross-run pollution).
func resetServiceRuntimeBarrier() {
	serviceRuntimeMu.Lock()
	serviceRuntimeStopping = false
	serviceRuntimeStarting = 0
	serviceRuntimeMu.Unlock()
}

// serviceRuntimeInflight reports the number of ServiceStart executions
// between their pre-check and post-check (tests and diagnostics).
func serviceRuntimeInflight() int {
	serviceRuntimeMu.Lock()
	defer serviceRuntimeMu.Unlock()
	return serviceRuntimeStarting
}

// serviceStartPause is a test seam (production leaves it nil): called inside
// ServiceStart after every validation and immediately before the
// runtime-creation barrier pre-check, so a test can suspend a Start that has
// already passed handler admission, both slot waits and the request-context
// re-check — the exact suspension point of the Start-vs-shutdown race.
var serviceStartPause func()

// serviceStartDelegate performs the runtime-creating phase of ServiceStart.
// Production delegates to the legacy Start; tests stub it to stage the
// Start-vs-shutdown race deterministically from INSIDE the creation phase
// (past the pre-check) — a point no config-driven pause can reach, and the
// exact window the shutdown mark must cover without stalling the stop path.
var serviceStartDelegate = func(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
	return globalServer.Start(ctx, in)
}

// serviceHandlerGate is the shutdown/admission barrier for one service run.
// It closes handler admission synchronously at shutdown and makes a
// WaitGroup.Add after the shutdown Wait has become possible impossible:
//
//	admit() and beginShutdown() share one mutex, so the two operations are
//	totally ordered. If admit() wins the critical section, its wg.Add(1) is
//	already counted before beginShutdown runs, and the Wait that may only be
//	started after beginShutdown returns observes it. If beginShutdown wins,
//	closing is set before any subsequent admit() runs (same mutex), so those
//	admits fail WITHOUT an Add. There is no third ordering. Hence: after
//	beginShutdown returns, no Add can happen, and the Wait observes exactly
//	the handlers admitted before shutdown.
//
// One gate per service run (created in Execute): the WaitGroup it owns dies
// with the run, so neither runs nor tests can pollute each other's counts.
type serviceHandlerGate struct {
	mu      sync.Mutex
	closing bool
	pending int // admitted handlers not yet finished (mirror of the wg count)
	wg      sync.WaitGroup
}

func newServiceHandlerGate() *serviceHandlerGate { return &serviceHandlerGate{} }

// admit admits one handler for execution: the caller may spawn a handler
// goroutine, which must eventually call release exactly once. After
// beginShutdown it always returns false and never touches the WaitGroup.
func (g *serviceHandlerGate) admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.pending++
	g.wg.Add(1)
	return true
}

// release pairs a successful admit.
func (g *serviceHandlerGate) release() {
	g.mu.Lock()
	g.pending--
	g.mu.Unlock()
	g.wg.Done()
}

// admitConn reports whether a newly accepted connection may start serving.
// It shares the gate's mutex, so once beginShutdown has run it is refused
// synchronously: a connection accepted in the Accept/shutdown race is closed
// here and never reaches a handshake. Unlike handlers, connections
// contribute no WaitGroup count — established connections are dropped by
// conns.closeAll (their serve loops exit on the next read), so they cannot
// stall the bounded shutdown, and idle connections keep their established
// semantics until shutdown closes them.
func (g *serviceHandlerGate) admitConn() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.closing
}

// beginShutdown closes admission synchronously and returns a channel that
// closes when every handler admitted before shutdown has finished. The wait
// must only be started on (or after) this call — that is what makes the
// Add-vs-Wait race impossible.
func (g *serviceHandlerGate) beginShutdown() <-chan struct{} {
	g.mu.Lock()
	g.closing = true
	g.mu.Unlock()
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	return done
}

// inflight reports the number of admitted handlers not yet finished (tests
// and diagnostics).
func (g *serviceHandlerGate) inflight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending
}

// serveServiceListener is the accept loop. Every client is authorized by
// token identity before it can reach the handshake, let alone a method.
//
// ctx is the service run's serve context; the SCM stop path cancels it
// synchronously AFTER closing admission, so the two shutdown signals the
// loop acts on (ctx cancel, stopCh, listener close) are ordered behind the
// admission barrier. A connection whose Accept succeeds in the Accept/close
// race is either admitted here before shutdown began (then its per-request
// gates and its serve context refuse every operation) or closed below —
// a connection never starts serving after admission has closed.
func serveServiceListener(ctx context.Context, listener net.Listener, conns *connSet, stopCh <-chan struct{}, admission *serviceHandlerGate) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			default:
			}
			log.Printf("service pipe accept error: %v", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if !authorizeServiceClient(conn) {
			_ = conn.Close()
			continue
		}
		if !conns.tryAdd(conn) {
			_ = conn.Close()
			continue
		}
		// Connection admission shares the lifecycle gate with handler
		// admission: once shutdown has begun, the connection is closed
		// here — before any handshake can start.
		if !admission.admitConn() {
			conns.remove(conn)
			_ = conn.Close()
			return
		}
		go func() {
			defer conns.remove(conn)
			serveServiceConnContext(ctx, conn, admission)
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

type proxyCoreServiceHandler struct {
	// admission is this run's lifecycle gate, created in Execute and handed
	// to the accept loop; the stop path closes it. A field (not a package
	// variable) so each service run — and each test driving one — owns its
	// own barrier and no run can pollute another's WaitGroup.
	admission *serviceHandlerGate
}

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

	// The run's serve context: canceled synchronously by the stop path below
	// (never via a watcher goroutine, whose delay would leave a window where
	// serve loops still run after shutdown began).
	serveCtx, serveCtxCancel := context.WithCancel(context.Background())
	defer serveCtxCancel()
	h.admission = newServiceHandlerGate()
	resetServiceRuntimeBarrier()

	go serveServiceListener(serveCtx, listener, conns, stopCh, h.admission)

	status <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		c := <-r
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending, WaitHint: 10000}
			// Coordinated shutdown sequence; Stopped is reported only after
			// it completes, and every step is synchronous and bounded:
			//  1. close admission — from here no handler can be admitted
			//     (and no WaitGroup.Add can happen), and no newly accepted
			//     connection starts serving;
			//  2. interrupt every serve loop and the accept loop (context,
			//     stop channel, listener);
			//  3. drop established connections (their serve loops exit on
			//     the next read — the client disconnect is a normal event);
			//  4. raise the runtime shutdown mark — O(1) flag work under
			//     serviceRuntimeMu, never across a runtime creation, so a
			//     slow or hanging in-flight Start cannot stall it (F7);
			//  5. stop the runtime (idempotent by upstream contract) —
			//     ordered AFTER the mark; a Start that finishes past the
			//     mark tears its own runtime down in its post-check and
			//     refuses, so no runtime outlives Stopped except through
			//     that bounded self-teardown;
			//  6. wait, bounded, for the handlers admitted before shutdown —
			//     a handler that will not finish in time cannot hold SCM
			//     hostage (the process exits right after Stopped anyway).
			stopOnce.Do(func() {
				handlersDone := h.admission.beginShutdown()
				serveCtxCancel()
				close(stopCh)
				_ = listener.Close()
				conns.closeAll()
				beginServiceRuntimeShutdown()
				// Stop is idempotent by upstream contract (no instance → nil error).
				_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
				select {
				case <-handlersDone:
				case <-time.After(2 * time.Second):
				}
			})
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

// serveServiceConnContext serves one client connection with the PC-110
// envelope protocol. A disconnect is a normal service-mode event: unlike
// runDispatch, it never terminates the process. admission is the service
// run's lifecycle gate; the caller (the accept loop) has already admitted the
// connection through it.
//
// EVERY frame — the mandatory Hello included — goes through the same
// validation pipeline, in one place, with no diverging handshake branch:
//
//	protocol version → shutdown gate → handshake state → request_id →
//	deadline → expected config-policy revision → operation registry →
//	typed payload → request-id dedup → handler admission → deadline-aware
//	slot waits → request-context re-check → dispatch
//
// The inline handshake skips only the admission/slot stages (it holds no
// slots and contributes no WaitGroup count) and compensates with its own
// reliable final gate: dispatchHandshake re-checks the serve context and the
// request deadline immediately before op.call, so a Hello decoded after
// shutdown began — or whose deadline expired since the serve loop's check —
// never dispatches, and the handshake is never established after shutdown
// began.
//
// Requests the serve loop itself refuses (unknown operation, wrong payload,
// stale revision, expired deadline, duplicate id) never reach a handler and
// never break the connection, so the client can correct and continue.
// Requests whose deadline expires while queued for a handler slot are
// refused the same way (code 5), and the slot that freed up cannot
// resurrect them: the request context is re-checked immediately before
// op.call, in the handler goroutine itself.
func serveServiceConnContext(ctx context.Context, conn net.Conn, admission *serviceHandlerGate) {
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

	// dispatchHandshake runs the connection's first Hello — already validated
	// by the pipeline — and answers the client. It is a separate function so
	// it can OWN the frame: releaseServiceEnvelope runs exactly once on every
	// path via defer, including the failed-answer path that previously
	// returned without releasing and stranded the frame's weight in the
	// aggregate payload budget (a leak a few connections could compound).
	//
	// The dispatch is guarded by a reliable final gate, run immediately
	// before op.call — after every validation step, with nothing waited on in
	// between (the inline handshake holds no slots, so the waits other
	// requests do cannot defer the check): a Hello decoded after shutdown
	// began, or whose deadline expired after the serve loop's deadline check,
	// never reaches the registry entry — code 7 (shutdown disconnects, and no
	// handshake is established after shutdown began), code 5 (late deadline;
	// the connection stays usable for a corrected retry). The id stays
	// consumed either way — the request was already accepted for execution,
	// the same conservative rule as a request refused after admission.
	// A shutdown that begins DURING the call still does not establish the
	// handshake: handshakeDone is never set after shutdown began.
	//
	// keep=false tells the caller to stop serving the connection; done=true
	// means the handshake was established.
	dispatchHandshake := func(frame *serviceEnvelopeFrame, op serviceOperationDef, reqMsg proto.Message, reqID uint64) (keep, done bool) {
		defer releaseServiceEnvelope(frame)

		hctx, cancel := envelopeContext(ctx, frame.env)
		defer cancel()

		if ctx.Err() != nil {
			_ = respond(reqID, envelopeCodeServiceUnavailable, "service is shutting down", nil)
			return false, false
		}
		if hctx.Err() != nil {
			_ = respond(reqID, envelopeCodeDeadlineExceeded,
				fmt.Sprintf("%s: request %d deadline expired before dispatch", errDeadlineExceeded, reqID), nil)
			return true, false
		}
		respMsg, callErr := callServiceOperation(op, hctx, reqMsg)
		if callErr != nil {
			_ = respond(reqID, envelopeCodeForHandlerError(callErr), callErr.Error(), nil)
			return false, false
		}
		respBytes, marshalErr := proto.Marshal(respMsg)
		if marshalErr != nil {
			return false, false
		}
		if ctx.Err() != nil {
			_ = respond(reqID, envelopeCodeServiceUnavailable, "service is shutting down", nil)
			return false, false
		}
		if err := respond(reqID, envelopeCodeOK, "", respBytes); err != nil {
			// The answer failed (client went away, write timeout): the defer
			// above has already returned the frame's weight to the budget.
			return false, false
		}
		return true, true
	}

	ids := newServiceRequestIDs()
	// Per-connection handler concurrency: every spawned handler holds one
	// token for its lifetime; the deadline-aware wait below parks requests
	// beyond the bound without dispatching them.
	handlerSem := make(chan struct{}, serviceHandlerConcurrencyPerConn)

	handshakeDone := false
	for {
		// The first frame is the handshake attempt: read with the handshake
		// timeout, not the idle timeout.
		frame, err := readServiceEnvelope(ctx, conn, handshakeDone)
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
			// A client that speaks another version (from the first frame on)
			// is incompatible; answer and disconnect.
			answer(&frame, reqID, envelopeCodeIncompatibleVersion,
				fmt.Sprintf("%s: client %d, service %d", errProtocolVersion, env.GetProtocolVersion(), serviceProtocolVersion))
			return
		}

		// Shutdown refuses new work — a first Hello included: nothing that
		// arrives from here on reaches a handler or establishes a session.
		if ctx.Err() != nil {
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}

		if !handshakeDone {
			// The first frame must be the Hello operation; anything else
			// means the client never speaks this protocol's handshake.
			if env.GetOperation() != "Hello" {
				answer(&frame, reqID, envelopeCodeInvalidRequest,
					fmt.Sprintf("%s: first request must be the Hello operation", errNoHandshake))
				return
			}
		} else if env.GetOperation() == "Hello" {
			if !answer(&frame, reqID, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: handshake already complete", errNoHandshake)) {
				return
			}
			continue
		}

		if reqID == 0 {
			// request_id is the dedup and correlation key; 0 is its absence.
			// The handshake obeys the same rule as every other frame.
			if !answer(&frame, 0, envelopeCodeInvalidRequest,
				fmt.Sprintf("%s: request_id is required", errInvalidRequest)) {
				return
			}
			continue
		}

		if ms := env.GetDeadlineUnixMs(); ms > 0 && !time.Now().Before(time.UnixMilli(ms)) {
			// An expired deadline — on the handshake too — is refused before
			// anything executes: no handshake is established, and the id is
			// free for a corrected retry.
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
		// so a client can retry a refused request with the same id. A request
		// refused LATER (its deadline expired while queued) has already been
		// accepted, so its id stays consumed and a retry needs a fresh id.
		if !ids.tryAdd(reqID) {
			if !answer(&frame, reqID, envelopeCodeDuplicateRequest,
				fmt.Sprintf("%s: request id %d was already executed on this connection", errDuplicateRequest, reqID)) {
				return
			}
			continue
		}

		if !handshakeDone {
			keep, done := dispatchHandshake(&frame, op, reqMsg, reqID)
			if !keep {
				return
			}
			handshakeDone = done
			continue
		}

		// The request context is derived BEFORE the slot waits so the client
		// deadline participates in them: a request whose deadline expires
		// while queued never reaches a handler. Shutdown still aborts both
		// waits (hctx is derived from the serve context).
		hctx, cancel := envelopeContext(ctx, env)

		// Handler admission under the lifecycle gate: once shutdown has
		// begun this fails synchronously, so no WaitGroup.Add can happen
		// after the shutdown Wait has become possible (serviceHandlerGate).
		if !admission.admit() {
			cancel()
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}

		// Handler concurrency is bounded both per connection and across the
		// service. Each wait releases exactly what it acquired — nothing
		// more, nothing less — and never spawns a handler after the request
		// context died.
		select {
		case serviceHandlerSlots <- struct{}{}:
		case <-hctx.Done():
			admission.release()
			cancel()
			if ctx.Err() == nil {
				if !answer(&frame, reqID, envelopeCodeDeadlineExceeded,
					fmt.Sprintf("%s: request %d deadline expired while waiting for a handler slot", errDeadlineExceeded, reqID)) {
					return
				}
				continue
			}
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}
		select {
		case handlerSem <- struct{}{}:
		case <-hctx.Done():
			<-serviceHandlerSlots
			admission.release()
			cancel()
			if ctx.Err() == nil {
				if !answer(&frame, reqID, envelopeCodeDeadlineExceeded,
					fmt.Sprintf("%s: request %d deadline expired while waiting for a handler slot", errDeadlineExceeded, reqID)) {
					return
				}
				continue
			}
			answer(&frame, reqID, envelopeCodeServiceUnavailable, "service is shutting down")
			return
		}

		// The payload budget travels with the handler so the aggregate
		// in-flight envelope memory stays bounded for the handler's lifetime.
		weight := frame.weight
		frame.weight = 0
		go func(requestID uint64, op serviceOperationDef, msg proto.Message) {
			// Ownership of admission, cancel and both slots transfers here:
			// every path below releases each exactly once.
			defer admission.release()
			defer cancel()
			defer func() { <-handlerSem }()
			defer func() { <-serviceHandlerSlots }()
			defer func() { servicePayloadSlots.Release(weight) }()
			if hctx.Err() != nil {
				// The request context died between admission and execution.
				// This is the reliable check immediately before op.call: a
				// request whose deadline expired in the queue — or during
				// shutdown — runs nothing, no matter how the waits raced.
				if ctx.Err() != nil {
					_ = respond(requestID, envelopeCodeServiceUnavailable, "service is shutting down", nil)
					return
				}
				_ = respond(requestID, envelopeCodeDeadlineExceeded,
					fmt.Sprintf("%s: request %d deadline expired before dispatch", errDeadlineExceeded, requestID), nil)
				return
			}
			respMsg, callErr := callServiceOperation(op, hctx, msg)
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

// Hello and Health are the service-mode additive methods: NOT part of the
// proto LibcoreService service block, so the generated server interface (and
// the non-Windows builds) are unaffected; on Windows they are bound into the
// serviceOperations registry (Hello and Health entries) and, for the legacy
// GUI-child table compatibility, into handlers below.

// Hello is the service-mode handshake operation — THE single Hello
// semantics, bound into serviceOperations and dispatched there: the serve
// loop's handshake phase calls this very registry entry after the envelope
// pipeline has already gated the protocol version, so there is no second
// HandshakeResp construction and no second version check. The envelope's
// protocol_version is the single version authority (mismatch/absence → code 1
// + disconnect, before any dispatch); HandshakeReq.protocol_version is not
// consulted — it exists for wire compatibility with the PC-100 spike's
// handshake and is documented as ignored. Hello carries no privileged
// effect: it returns the service identity.
func (s *server) Hello(ctx context.Context, in *gen.HandshakeReq) (*gen.HandshakeResp, error) {
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
	// Runtime-creation barrier against the service shutdown (F7). The stop
	// path raises the shutdown mark in O(1) and never waits for a creation;
	// this side mirrors it in two O(1) steps around the delegation, which
	// runs WITHOUT holding the barrier lock:
	//   - pre-check: past the mark → refuse immediately (no boxmain.Create,
	//     no Xray instance, no extra process);
	//   - post-check: finished past the mark → tear down whatever the
	//     delegation published (the stop path's final Stop may already have
	//     run) and refuse with errServiceStopping (envelope code 7).
	// Total order per Start: refused-before-creation, or created-then-torn-
	// down. The teardown is idempotent (upstream Stop contract) and runs
	// WITHOUT the barrier lock. No lock is ever held across another: the
	// barrier lock is taken alone for flag/counter work, lifecycleMu alone
	// inside the delegate/teardown — so no nesting, no deadlock. The legacy Start itself is untouched (protected file): the
	// GUI-child path never sees the mark and behaves byte-identically; the
	// mark, the checks and the teardown all live on the service path.
	// Residual, documented: a creation that outlives the stop path's bounded
	// handlers wait finishes its self-teardown after Stopped is published —
	// transient by construction (the handler that performs it is already
	// counted in the wait), never a persisting runtime.
	if serviceStartPause != nil {
		serviceStartPause()
	}
	serviceRuntimeMu.Lock()
	if serviceRuntimeStopping {
		serviceRuntimeMu.Unlock()
		return nil, fmt.Errorf("%w: start refused", errServiceStopping)
	}
	serviceRuntimeStarting++
	serviceRuntimeMu.Unlock()
	defer func() {
		serviceRuntimeMu.Lock()
		serviceRuntimeStarting--
		serviceRuntimeMu.Unlock()
	}()
	out, startErr := serviceStartDelegate(ctx, in)
	serviceRuntimeMu.Lock()
	stopping := serviceRuntimeStopping
	serviceRuntimeMu.Unlock()
	if stopping {
		_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		return nil, fmt.Errorf("%w: start refused", errServiceStopping)
	}
	return out, startErr
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
