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
	"sync"
	"time"

	"github.com/tailscale/go-winio"
	C "github.com/sagernet/sing-box/constant"
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
)

func servicePipeName() string {
	if v := os.Getenv("THRONE_SERVICE_PIPE"); v != "" {
		return v
	}
	return defaultServicePipeName
}

func serviceSDDL() string {
	if v := os.Getenv("THRONE_SERVICE_SDDL"); v != "" {
		return v
	}
	return defaultServiceSDDL
}

// applyServiceModeSettings pins spike-level runtime settings. The GUI child
// mode prints full core configs when THRONE_CORE_DEBUG=1 (documented upstream
// debt); service mode never does.
func applyServiceModeSettings() {
	debug = false
}

func listenServicePipe() (net.Listener, error) {
	return winio.ListenPipe(servicePipeName(), &winio.PipeConfig{
		SecurityDescriptor: serviceSDDL(),
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

	go func() {
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
			conns.add(conn)
			go func() {
				defer conns.remove(conn)
				serveServiceConn(conn)
			}()
		}
	}()

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
					log.Printf("panic in %s: %v", method, r)
					_ = writeServiceResponse(&writeMu, conn, id, 1,
						[]byte(fmt.Sprintf("core panic in %s: %v", method, r)))
				}
			}()
			respData, dispatchErr := dispatch(method, pl)
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

func init() {
	handlers["Hello"] = handle(globalServer.Hello)
	handlers["Health"] = handle(globalServer.Health)
}
