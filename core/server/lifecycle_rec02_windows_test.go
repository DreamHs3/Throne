//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"ThroneCore/gen"
	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

// REC-02 R3 regression: the SECOND Start must return an error while the
// running runtime, its cancel and the redirect mark stay untouched, and a
// subsequent Stop must close that runtime. The pre-fix deferred cleanup ran
// for the rejected duplicate Start too and orphaned the live box.
func TestDuplicateStartPreservesRunningRuntime(t *testing.T) {
	t.Cleanup(func() {
		_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		autoRedirectMark.Store(0)
	})

	req := &gen.LoadConfigReq{CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`)}
	// The legacy Start dereferences proto2 optional fields directly; the
	// service path materializes the defaults through normalizeLoadConfigReq,
	// and this test drives the legacy path directly, so it does the same.
	normalizeLoadConfigReq(req)
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("the first start did not publish the runtime")
	}
	autoRedirectMark.Store(7777)

	resp, err := globalServer.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("duplicate start returned a transport error: %v", err)
	}
	if resp.GetError() != "instance already started" {
		t.Fatalf("duplicate start error = %q, want %q", resp.GetError(), "instance already started")
	}
	if currentBox() == nil {
		t.Fatal("the duplicate Start erased the running runtime reference (REC-02 R3)")
	}
	if got := autoRedirectMark.Load(); got != 7777 {
		t.Fatalf("the duplicate Start reset the redirect mark: got %d, want 7777 (REC-02 R3)", got)
	}

	if _, err := globalServer.Stop(context.Background(), &gen.EmptyReq{}); err != nil {
		t.Fatalf("stop after duplicate start: %v", err)
	}
	if currentBox() != nil {
		t.Fatal("the subsequent stop did not close the runtime")
	}

	// REC-02 item 5: repeated Stop stays idempotent.
	if _, err := globalServer.Stop(context.Background(), &gen.EmptyReq{}); err != nil {
		t.Fatalf("repeated stop must stay nil-error, got %v", err)
	}

	// REC-02 item 5: a real startup error must keep the no-instance state -
	// the error-cleanup path runs only for the attempt that owns the state.
	badReq := &gen.LoadConfigReq{CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[{"type":"no_such_inbound"}]}`)}
	normalizeLoadConfigReq(badReq)
	if resp, err := globalServer.Start(context.Background(), badReq); err == nil && resp.GetError() == "" {
		t.Fatal("an invalid core config must fail the start")
	}
	if currentBox() != nil {
		t.Fatal("a failed start left a runtime published")
	}
}

// REC-02 R2: the stop path with a REAL blocking hold on the lifecycle lock
// (a Start parked inside boxmain.Create) must still reach Stopped, bounded.
// REC-02B: because the runtime stop cannot run (the lock hold starves its
// bounded lock wait), the cleanup stays UNCONFIRMED — Execute must record
// that as the service-specific exit code svcExitStopUnconfirmed, never as a
// successful stop, and log the emergency path (the process exit after
// Stopped destroys the in-process runtime). The review rejected the naive
// "Stop in a goroutine + ACK": the test also pairs the bound with the F7
// shutdown mark - after Execute returns, the mark must be raised so a late
// Start is refused and no runtime is published.
func TestServiceExecuteStopBoundedWithHeldLifecycleLock(t *testing.T) {
	t.Setenv("THRONE_SERVICE_PIPE", `\\.\pipe\ProxyCoreServiceTest-StopHeldLock`)
	selfSID, _ := currentProcessIdentity(t)
	t.Setenv("THRONE_SERVICE_SDDL", "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;"+selfSID+")")
	t.Setenv("THRONE_SERVICE_ALLOWED_SIDS", selfSID)
	resetServiceRuntimeStoppingForTest(t)

	// The real blocking: a Start inside boxmain.Create holds the lifecycle
	// lock for the whole (hung) creation. Hold it exactly like that.
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

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

	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case fire := <-exited:
		if fire {
			t.Fatal("a normal SCM stop must not request service death")
		}
		// REC-02B: the stop could not run under the held lock, so the
		// cleanup is unconfirmed — the exit code must record an abnormal
		// stop, not a clean one.
		if code := <-exitCode; code != svcExitStopUnconfirmed {
			t.Fatalf("an unconfirmed stop exit code = %d, want %d (a bounded but unconfirmed stop must not be recorded as clean, REC-02B)", code, svcExitStopUnconfirmed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return after a Stop request while the lifecycle lock was held (unbounded stop, REC-02 R2)")
	}

	serviceRuntimeMu.Lock()
	stopping := serviceRuntimeStopping
	serviceRuntimeMu.Unlock()
	if !stopping {
		t.Fatal("the shutdown mark must stay raised after the bounded stop")
	}

	// The mark must refuse a start that arrives after the bounded stop, so
	// no new runtime can be published past Stopped.
	_, err := globalServer.ServiceStart(context.Background(), &gen.LoadConfigReq{CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`)})
	if !errors.Is(err, errServiceStopping) {
		t.Fatalf("a start after the bounded stop must be refused with errServiceStopping, got %v", err)
	}
}
