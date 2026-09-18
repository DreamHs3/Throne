//go:build windows

package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/boxbox"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

// REC-02B regressions: the bounded close wait must never be mistaken for a
// completed teardown. The close result is explicit (completed/error/timeout),
// a close that outlives the budget stays OWNED by the stop path until it
// really finishes (Start refused, repeated Stop not clean), and the SCM
// boundary consumes the stop result — a failed or over-budget stop is
// recorded as an abnormal service exit, never as a successful stop.

const rec02bCoreConfig = `{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`

// ---- Stop × a hanging runtime close ----

// A close that outlives the budget must make Stop report a timeout (never a
// clean stop), keep ownership of the closing runtime in the teardown record,
// refuse a Start that would build a second runtime on top of the closing one,
// make a repeated Stop report the unfinished teardown, and restore the state
// at the real close completion (record cleared, Start and clean Stop work
// again).
func TestStopReportsTimeoutWhenCloseHangs(t *testing.T) {
	t.Cleanup(func() {
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
		autoRedirectMark.Store(0)
	})

	req := &gen.LoadConfigReq{CoreConfig: proto.String(rec02bCoreConfig)}
	normalizeLoadConfigReq(req)
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("the first start did not publish the runtime")
	}

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var hangOnce, enteredOnce, releaseOnce sync.Once
	prevClose := closePublishedRuntime
	closePublishedRuntime = func(box *boxbox.Box, cancel context.CancelFunc, d time.Duration, onDone func(error)) (boxbox.CloseOutcome, error) {
		hang := false
		hangOnce.Do(func() { hang = true })
		if !hang {
			return prevClose(box, cancel, d, onDone)
		}
		// The close hangs: give up the wait like the real bounded close,
		// keep the close "running" until released, then finish it for real
		// and report the completion through the hook — the record must be
		// cleared only at that point.
		enteredOnce.Do(func() { close(entered) })
		go func() {
			<-releaseCh
			cancel()
			_ = box.Close()
			onDone(nil)
		}()
		return boxbox.CloseTimedOut, nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		closePublishedRuntime = prevClose
		deadline := time.Now().Add(5 * time.Second)
		for pendingTeardown() != nil && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})

	// Stop #1: the close hangs -> the stop reports a timeout.
	stop1 := make(chan *gen.ErrorResp, 1)
	go func() {
		resp, _ := globalServer.Stop(context.Background(), &gen.EmptyReq{})
		stop1 <- resp
	}()
	select {
	case resp := <-stop1:
		if resp.GetError() == "" {
			t.Fatal("a hung close must not be reported as a clean stop (REC-02B)")
		}
		if !strings.Contains(resp.GetError(), "stop timed out") {
			t.Fatalf("a hung close must be reported as a stop timeout, got %q", resp.GetError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not return while the close hung")
	}
	if currentBox() != nil {
		t.Fatal("the stop must have unpublished the closing runtime")
	}
	if pendingTeardown() == nil {
		t.Fatal("the timed-out close must stay owned by the teardown record (REC-02B)")
	}

	// Start during the unfinished close: refused, no second runtime.
	startResp, startErr := globalServer.Start(context.Background(), req)
	if startErr != nil {
		t.Fatalf("the refused start must not carry a transport error: %v", startErr)
	}
	if !strings.Contains(startResp.GetError(), "teardown is still in progress") {
		t.Fatalf("a start during an unfinished close must be refused with the teardown error, got %q", startResp.GetError())
	}
	if currentBox() != nil {
		t.Fatal("a start during an unfinished close created a second runtime (REC-02B)")
	}

	// A repeated Stop must see the unfinished close instead of reporting a
	// clean stop.
	stop2 := make(chan *gen.ErrorResp, 1)
	go func() {
		resp, _ := globalServer.Stop(context.Background(), &gen.EmptyReq{})
		stop2 <- resp
	}()
	select {
	case resp := <-stop2:
		if !strings.Contains(resp.GetError(), "previous runtime close is still running") {
			t.Fatalf("a repeated stop must report the unfinished teardown, got %q", resp.GetError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the repeated stop did not return")
	}
	if pendingTeardown() == nil {
		t.Fatal("the repeated stop must leave the unfinished close owned")
	}

	// Recovery: the close finishes -> the record clears at the real
	// completion -> Start works again and a clean Stop follows.
	releaseOnce.Do(func() { close(releaseCh) })
	deadline := time.Now().Add(10 * time.Second)
	for pendingTeardown() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pendingTeardown() != nil {
		t.Fatal("the teardown record must clear at the real close completion")
	}
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("start after the teardown completed: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("no runtime after the post-recovery start")
	}
	if _, err := globalServer.Stop(context.Background(), &gen.EmptyReq{}); err != nil {
		t.Fatalf("the stop after recovery must be clean, got %q", err)
	}
	if currentBox() != nil {
		t.Fatal("runtime left after the clean stop")
	}
}

// A close that finishes within the budget but FAILS must surface its error:
// Stop reports it instead of an empty (successful) response, and no teardown
// record is registered (nothing is left running in the background).
func TestStopReportsCloseErrorNotClean(t *testing.T) {
	sentinel := errors.New("synthetic close failure")

	req := &gen.LoadConfigReq{CoreConfig: proto.String(rec02bCoreConfig)}
	normalizeLoadConfigReq(req)
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("first start: %v", err)
	}

	var closedBox *boxbox.Box
	var closedCancel context.CancelFunc
	prevClose := closePublishedRuntime
	closePublishedRuntime = func(box *boxbox.Box, cancel context.CancelFunc, d time.Duration, onDone func(error)) (boxbox.CloseOutcome, error) {
		closedBox, closedCancel = box, cancel
		return boxbox.CloseFailed, sentinel
	}
	t.Cleanup(func() {
		closePublishedRuntime = prevClose
		// The stub skipped the real close: republish and stop for real so
		// the test leaks no runtime.
		if closedBox != nil {
			setBoxInstance(closedBox, closedCancel)
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
		autoRedirectMark.Store(0)
	})

	resp, err := globalServer.Stop(context.Background(), &gen.EmptyReq{})
	if err != nil {
		t.Fatalf("stop must report the failure in the response, not as a transport error: %v", err)
	}
	if !strings.Contains(resp.GetError(), sentinel.Error()) {
		t.Fatalf("a failed close must surface its error, got %q", resp.GetError())
	}
	if currentBox() != nil {
		t.Fatal("the failed close must still have unpublished the runtime")
	}
	if pendingTeardown() != nil {
		t.Fatal("a finished (failed) close must not leave a teardown record")
	}
}

// ---- the SCM boundary × the stop result ----

// executeHarness drives the REAL proxyCoreServiceHandler.Execute to a Stop
// without the SCM, collecting the exit code Execute returns.
type executeHarness struct {
	requests chan svc.ChangeRequest
	statuses chan svc.Status
	handler  *proxyCoreServiceHandler
	exited   <-chan bool
	exitCode <-chan uint32
}

func startExecute(t *testing.T, name string) *executeHarness {
	t.Helper()
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-`+name)
	selfSID, _ := currentProcessIdentity(t)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
	resetServiceRuntimeStoppingForTest(t)

	h := &executeHarness{
		requests: make(chan svc.ChangeRequest),
		statuses: make(chan svc.Status, 16),
		handler:  &proxyCoreServiceHandler{},
	}
	exited := make(chan bool, 1)
	exitCode := make(chan uint32, 1)
	go func() {
		fire, code := h.handler.Execute(nil, h.requests, h.statuses)
		exited <- fire
		exitCode <- code
	}()
	h.exited = exited
	h.exitCode = exitCode

	deadline := time.After(10 * time.Second)
	for {
		select {
		case s := <-h.statuses:
			if s.State == svc.Running {
				return h
			}
		case <-deadline:
			t.Fatal("service never reached RUNNING")
		}
	}
}

func (h *executeHarness) stop(t *testing.T, wantClean bool, maxWait time.Duration) {
	t.Helper()
	h.requests <- svc.ChangeRequest{Cmd: svc.Stop}
	// REC-02B: Execute no longer sends Stopped on the status channel (that
	// send finalized the SCM record with default exit codes before the
	// returned exit code could be attached — proven in the VM evidence).
	// The svc runtime reports SERVICE_STOPPED itself after Execute returns,
	// so the return (captured below) IS the stopped point of the harness.
	select {
	case <-h.exited:
		code := <-h.exitCode
		if wantClean && code != svcExitClean {
			t.Fatalf("a confirmed clean stop exit code = %d, want %d", code, svcExitClean)
		}
		if !wantClean && code != svcExitStopUnconfirmed {
			t.Fatalf("an unconfirmed stop exit code = %d, want %d (REC-02B: unconfirmed cleanup must not be recorded as a successful stop)", code, svcExitStopUnconfirmed)
		}
	case <-time.After(maxWait):
		t.Fatal("Execute did not return after a Stop request")
	}
}

// The SCM boundary consumes the stop result explicitly: a nil Go error AND an
// empty ErrorResp are the only confirmed cleanup (exit code 0); a Go error, a
// reported ErrorResp error, or a stop that outlives the boundary budget is
// unconfirmed and must be recorded as svcExitStopUnconfirmed — never as a
// successful stop.
func TestServiceExecuteStopResultAtBoundary(t *testing.T) {
	prevStop := serviceStopDelegate
	t.Cleanup(func() { serviceStopDelegate = prevStop })

	cases := []struct {
		name      string
		delegate  func(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error)
		wantClean bool
	}{
		{
			name: "clean stop confirms the cleanup",
			delegate: func(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
				return &gen.ErrorResp{}, nil
			},
			wantClean: true,
		},
		{
			name: "go error is not a clean stop",
			delegate: func(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
				return nil, errors.New("synthetic stop go error")
			},
			wantClean: false,
		},
		{
			name: "error resp is not a clean stop",
			delegate: func(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
				return &gen.ErrorResp{Error: proto.String("synthetic stop failure")}, nil
			},
			wantClean: false,
		},
		{
			name: "stop past the boundary budget is not a clean stop",
			delegate: func(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
				<-time.After(30 * time.Second) // outlives the boundary budget
				return &gen.ErrorResp{}, nil
			},
			wantClean: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serviceStopDelegate = tc.delegate
			h := startExecute(t, "StopResult-"+strings.ReplaceAll(tc.name, " ", ""))
			started := time.Now()
			h.stop(t, tc.wantClean, 15*time.Second)
			if elapsed := time.Since(started); elapsed > 12*time.Second {
				t.Fatalf("the bounded stop path took %v — shutdown is not bounded", elapsed)
			}
			if currentBox() != nil {
				t.Fatal("no runtime may exist after the service stop")
			}
		})
	}
}

// ---- late publication: a Start resumed after the SCM stop hit its budget ----

// The full review scenario a new-Start refusal cannot replace: a Start that
// already passed admission and the barrier pre-check is parked INSIDE the
// runtime creation (holding lifecycleMu exactly like the real creation does),
// the SCM stop path hits its budget against that hold, Stopped is published
// unconfirmed (svcExitStopUnconfirmed) — and THEN the creation resumes for
// real: it publishes the runtime LATE, and the post-check must tear it down
// and refuse. Nothing may survive: no published runtime, no teardown record,
// no Xray instance, no extra process.
func TestServiceExecuteStartInsideCreationNoLatePublication(t *testing.T) {
	realDelegate := serviceStartDelegate
	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	publishedLate := make(chan bool, 1)
	serviceStartDelegate = func(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
		// The real legacy Start holds lifecycleMu across the whole creation;
		// hold it exactly like that, so the stop path's bounded lock wait
		// expires and the SCM boundary hits its budget.
		lifecycleMu.Lock()
		enterOnce.Do(func() { close(entered) })
		<-releaseCh
		lifecycleMu.Unlock()
		// Resume the creation for real: the legacy Start builds and
		// publishes the runtime — the late publication the stop path must
		// not leave behind.
		out, err := realDelegate(ctx, in)
		publishedLate <- currentBox() != nil
		return out, err
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		serviceStartDelegate = realDelegate
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
	})

	h := startExecute(t, "InsideCreationLatePub")
	conn := dialServicePipe(t)
	defer func() { _ = conn.Close() }()
	mustHandshake(t, conn)

	envSend(t, conn, 100, serviceProtocolVersion, "Start", mustMarshal(t, &gen.LoadConfigReq{
		CoreConfig: proto.String(rec02bCoreConfig),
	}))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the Start never reached the runtime-creation phase")
	}
	// Past admission, past both slot waits: the handler is executing
	// ServiceStart, parked inside the creation with lifecycleMu held.
	waitAdmissionInflight(t, h.handler.admission, 1)

	// SCM stop against the held lock: the boundary must hit its budget,
	// record the cleanup as UNCONFIRMED, and still reach Stopped bounded.
	started := time.Now()
	h.requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case <-h.exited:
		if code := <-h.exitCode; code != svcExitStopUnconfirmed {
			t.Fatalf("exit code = %d, want %d (a stop that could not run must not be recorded as clean, REC-02B)", code, svcExitStopUnconfirmed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Execute did not return after a Stop request against a held lifecycle lock")
	}
	if elapsed := time.Since(started); elapsed > 12*time.Second {
		t.Fatalf("Stop took %v with a Start parked inside creation: shutdown is not bounded", elapsed)
	}
	// Execute returned: the svc runtime reports SERVICE_STOPPED at this point
	// (REC-02B: no explicit Stopped send from Execute).

	// The stop completed while the creation is STILL parked inside.
	if h.handler.admission.inflight() != 1 {
		t.Fatalf("the parked Start must still be in flight at Stopped, inflight = %d", h.handler.admission.inflight())
	}
	if !serviceRuntimeStopping {
		t.Fatal("the stop path must have raised the runtime shutdown mark")
	}
	if currentBox() != nil {
		t.Fatal("Stopped must be published with no runtime")
	}

	// Resume: the creation publishes LATE (that must actually happen — the
	// premise of the scenario), and the post-check must tear it down.
	releaseOnce.Do(func() { close(releaseCh) })
	select {
	case published := <-publishedLate:
		if !published {
			t.Fatal("the resumed creation must have actually published a runtime — the scenario premise failed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the resumed creation never finished")
	}
	// The handler that ran ServiceStart releases admission after the
	// post-check completed, so draining admission proves the teardown ran.
	waitAdmissionInflight(t, h.handler.admission, 0)
	deadline := time.Now().Add(5 * time.Second)
	for serviceRuntimeInflight() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := serviceRuntimeInflight(); got != 0 {
		t.Fatalf("runtime-creation inflight = %d, want 0 (leaked barrier count)", got)
	}
	if currentBox() != nil {
		t.Fatal("the late publication must be torn down by the post-check (REC-02B)")
	}
	if pendingTeardown() != nil {
		t.Fatal("no teardown record may remain after the post-check teardown")
	}
	if extraProcess != nil {
		t.Fatal("no extra process may exist after the post-mark teardown")
	}
	if instances := liveXrayInstances(); len(instances) != 0 {
		t.Fatalf("no Xray instance may exist after the post-mark teardown, got %d", len(instances))
	}
}
