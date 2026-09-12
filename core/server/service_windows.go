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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

	// Wire-level sanity guards for the new service path only; the formal
	// frame/request limit contract arrives with PC-110.
	serviceMaxMethodLen  = 4096
	serviceMaxPayloadLen = 32 << 20

	// Bound on concurrently executing handlers per connection; the formal
	// bounded-concurrency contract (including a global bound) is PC-110.
	serviceHandlerConcurrencyPerConn = 8
)

// The service pipe protocol version; incompatible clients get a typed error.
const serviceProtocolVersion = 1

// Machine-readable error prefixes for the spike handshake (PC-110 replaces
// this with the typed error code contract).
const (
	errNoHandshake      = "ERR_NO_HANDSHAKE"
	errProtocolVersion  = "ERR_PROTOCOL_VERSION"
	errInvalidRequest   = "ERR_INVALID_REQUEST"
	errMethodNotAllowed = "ERR_METHOD_NOT_ALLOWED"
)

// serviceMethodAllowlist is the ONLY method table a service-mode client can
// reach after the handshake. It deliberately does NOT fall back to the legacy
// handlers map: that table carries privileged utility RPC (SetSystemDNS,
// InstallDashboard, CloseConnections, ...) and, via Start, the extra-process
// execution surface — none of which a service client may drive. Remediation
// of the PC-100 review P0.
var serviceMethodAllowlist = map[string]handlerFn{
	"Hello":       handle(globalServer.Hello),
	"Health":      handle(globalServer.Health),
	"CheckConfig": handle(globalServer.ServiceCheckConfig),
	"Start":       handle(globalServer.ServiceStart),
	"Stop":        handle(globalServer.Stop),
}

// dispatchService serves a service-mode client from the allowlist only. Every
// other method — known to the legacy table or not — gets the same stable
// typed error, and the legacy GUI-child dispatch is untouched.
func dispatchService(method string, payload []byte) ([]byte, error) {
	h, found := serviceMethodAllowlist[method]
	if !found {
		return nil, fmt.Errorf("%s: %q is not available in service mode", errMethodNotAllowed, method)
	}
	return h(context.Background(), payload)
}

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

// safeServiceSDDL protects the SDDL override from unsafe substitution. A
// usable DACL must be protected ("D:P" - inherited ACEs off) and must not
// grant access to broad trustees: Everyone (WD), Anonymous (AN),
// Authenticated Users (AU) or Builtin Users (BU). Per-user SID grants
// ("A;;GA;;;S-1-5-21-...") and SYSTEM/ADMINISTRATORS remain fine. An unsafe
// override fails the listener start - never a silent fallback to something
// broader.
func safeServiceSDDL(sddl string) (string, error) {
	if !strings.HasPrefix(sddl, "D:P") {
		return "", fmt.Errorf("%s: service SDDL must start with a protected DACL (D:P)", errInvalidRequest)
	}
	for _, trustee := range []string{"WD", "AN", "AU", "BU"} {
		if sddlGrantsTrustee(sddl, trustee) {
			return "", fmt.Errorf("%s: service SDDL must not grant access to %s", errInvalidRequest, trustee)
		}
	}
	return sddl, nil
}

// sddlGrantsTrustee reports whether any allow ACE in the SDDL string grants
// access to the given trustee abbreviation. An ACE body is
// type;flags;rights;objectGUID;inheritGUID;trustee — the "(A;" split consumes
// the type and its delimiter, so the trustee is the last remaining field.
// Deliberately simple: exact match on the trustee field of each allow ACE.
func sddlGrantsTrustee(sddl string, trustee string) bool {
	for _, ace := range strings.Split(sddl, "(A;") {
		fields := strings.Split(strings.TrimSuffix(ace, ")"), ";")
		if len(fields) > 0 && fields[len(fields)-1] == trustee {
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

func (cs *connSet) add(c net.Conn) {
	cs.mu.Lock()
	cs.m[c] = struct{}{}
	cs.mu.Unlock()
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
		conns.add(conn)
		go func() {
			defer conns.remove(conn)
			serveServiceConn(conn)
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
		out = append(out, member.Sid.String())
	}
	return out
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

type serviceRequest struct {
	id      uint32
	method  string
	payload []byte
}

// readServiceRequest parses the same little-endian frame layout as
// runDispatch: [u32 reqId][u16 methodLen][method][u32 payloadLen][payload].
// Duplicated on purpose so dispatch.go stays untouched (ADR-002); PC-110
// replaces both with the versioned envelope reader.
func readServiceRequest(conn net.Conn) (serviceRequest, error) {
	var req serviceRequest
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return req, err
	}
	req.id = binary.LittleEndian.Uint32(head[:])

	var methodLenBytes [2]byte
	if _, err := io.ReadFull(conn, methodLenBytes[:]); err != nil {
		return req, err
	}
	methodLen := binary.LittleEndian.Uint16(methodLenBytes[:])
	if methodLen == 0 || methodLen > serviceMaxMethodLen {
		return req, fmt.Errorf("%s: method length %d out of range", errInvalidRequest, methodLen)
	}
	method := make([]byte, methodLen)
	if _, err := io.ReadFull(conn, method); err != nil {
		return req, err
	}
	req.method = string(method)

	var payloadLenBytes [4]byte
	if _, err := io.ReadFull(conn, payloadLenBytes[:]); err != nil {
		return req, err
	}
	payloadLen := binary.LittleEndian.Uint32(payloadLenBytes[:])
	if payloadLen > serviceMaxPayloadLen {
		return req, fmt.Errorf("%s: payload too large: %d", errInvalidRequest, payloadLen)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return req, err
	}
	req.payload = payload
	return req, nil
}

// writeServiceResponse mirrors runDispatch's response framing. mu is optional
// (nil for the handshake phase, which is single-threaded per connection).
func writeServiceResponse(mu *sync.Mutex, conn net.Conn, reqId uint32, status uint8, data []byte) error {
	var header [9]byte
	binary.LittleEndian.PutUint32(header[0:], reqId)
	header[4] = status
	binary.LittleEndian.PutUint32(header[5:], uint32(len(data)))
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := conn.Write(data)
		return err
	}
	return nil
}

// serveServiceConn handles one client connection. A disconnect is a normal
// service-mode event: unlike runDispatch, it never terminates the process.
func serveServiceConn(conn net.Conn) {
	defer conn.Close()

	// Mandatory versioned handshake before any other method.
	req, err := readServiceRequest(conn)
	if err != nil {
		return
	}
	if req.method != "Hello" {
		_ = writeServiceResponse(nil, conn, req.id, 1,
			[]byte(fmt.Sprintf("%s: first request must be Hello", errNoHandshake)))
		return
	}
	var hs gen.HandshakeReq
	if err := proto.Unmarshal(req.payload, &hs); err != nil {
		_ = writeServiceResponse(nil, conn, req.id, 1,
			[]byte(fmt.Sprintf("%s: %v", errInvalidRequest, err)))
		return
	}
	if hs.GetProtocolVersion() != serviceProtocolVersion {
		_ = writeServiceResponse(nil, conn, req.id, 1,
			[]byte(fmt.Sprintf("%s: client %d, service %d", errProtocolVersion, hs.GetProtocolVersion(), serviceProtocolVersion)))
		return
	}
	hsResp, err := proto.Marshal(&gen.HandshakeResp{
		ProtocolVersion: To(int32(serviceProtocolVersion)),
		ServiceVersion:  To(C.Version),
	})
	if err != nil {
		return
	}
	if err := writeServiceResponse(nil, conn, req.id, 0, hsResp); err != nil {
		return
	}

	var writeMu sync.Mutex
	sem := make(chan struct{}, serviceHandlerConcurrencyPerConn)
	for {
		req, err := readServiceRequest(conn)
		if err != nil {
			// Client went away or sent garbage: close and move on.
			return
		}
		if req.method == "Hello" {
			_ = writeServiceResponse(&writeMu, conn, req.id, 1,
				[]byte(fmt.Sprintf("%s: handshake already complete", errInvalidRequest)))
			continue
		}
		sem <- struct{}{}
		serviceHandlersWG.Add(1)
		go func(id uint32, method string, pl []byte) {
			defer serviceHandlersWG.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					// Same stack parity as runDispatch: a panic must be diagnosable.
					log.Printf("panic in %s: %v\n%s", method, r, runtimeDebug.Stack())
					_ = writeServiceResponse(&writeMu, conn, id, 1,
						[]byte(fmt.Sprintf("core panic in %s: %v", method, r)))
				}
			}()
			respData, dispatchErr := dispatchService(method, pl)
			if dispatchErr != nil {
				_ = writeServiceResponse(&writeMu, conn, id, 1, []byte(dispatchErr.Error()))
			} else {
				_ = writeServiceResponse(&writeMu, conn, id, 0, respData)
			}
		}(req.id, req.method, req.payload)
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
