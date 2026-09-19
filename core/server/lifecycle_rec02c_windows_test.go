//go:build windows

package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/boxbox"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// REC-02C regressions: the teardown record is registered BEFORE the bounded
// close starts, and a CloseTimedOut return must never re-register it. The
// pre-fix order (register only on the CloseTimedOut return) raced the onDone
// hook: a close finishing right at the budget boundary runs the hook while
// the bounded wait still returns CloseTimedOut, the hook's identity-checked
// clear misses the not-yet-registered record, and the re-registration pins a
// COMPLETED close in the record. A genuinely incomplete close keeps blocking
// Start and the repeated Stop — that coverage stays in
// TestStopReportsTimeoutWhenCloseHangs.

// The deterministic interleaving of that race: the completion callback has
// ALREADY run (nothing is left running in the background) when
// closePublishedRuntime returns CloseTimedOut. No sleeps: the stub runs the
// hook inline before the return. On the pre-fix order this leaves the
// completed record registered and the next Start locked; with the record
// registered before the close, the hook's clear lands on it and
// CloseTimedOut does not re-register — the next Start needs no extra Stop.
func TestStopWithCompletedCloseTimedOutLeavesNoRecord(t *testing.T) {
	req := &gen.LoadConfigReq{CoreConfig: proto.String(rec02bCoreConfig)}
	normalizeLoadConfigReq(req)
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("first start: %v", err)
	}

	// Everything the test created is freed here — including on an early
	// t.Fatal: the captured first runtime is really closed if the modeled
	// Stop never got to it, whatever instance is still published is
	// stopped, and the standard close function is restored. (Cleanup runs
	// strictly after the test body, so a plain flag cannot race.)
	var capturedBox *boxbox.Box
	var capturedCancel context.CancelFunc
	capturedClosed := false
	prevClose := closePublishedRuntime
	modelingStop := false
	closePublishedRuntime = func(box *boxbox.Box, cancel context.CancelFunc, d time.Duration, onDone func(error)) (boxbox.CloseOutcome, error) {
		// The stub serves ONLY the modeled Stop; any other close (a later
		// Start/Stop that races a failure, the cleanup's own Stop) goes
		// through the real bounded close.
		if !modelingStop {
			return prevClose(box, cancel, d, onDone)
		}
		capturedBox, capturedCancel = box, cancel
		onDone(nil)
		return boxbox.CloseTimedOut, nil
	}
	t.Cleanup(func() {
		modelingStop = false
		closePublishedRuntime = prevClose
		if capturedBox != nil && !capturedClosed {
			_, _ = capturedBox.CloseWithTimeout(capturedCancel, 2*time.Second, log.Println, nil)
		}
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
		autoRedirectMark.Store(0)
	})

	if currentBox() == nil {
		t.Fatal("the first start did not publish the runtime")
	}

	// The stub skips the real close (like the failed-close stub in REC-02B):
	// the completion callback runs inline, then the bounded wait reports the
	// timeout — exactly the budget-boundary interleaving.
	modelingStop = true
	resp, err := globalServer.Stop(context.Background(), &gen.EmptyReq{})
	modelingStop = false
	closePublishedRuntime = prevClose
	if err != nil {
		t.Fatalf("stop must report the outcome in the response, not as a transport error: %v", err)
	}
	// The bounded wait timed out and the caller still sees exactly that.
	if !strings.Contains(resp.GetError(), "stop timed out") {
		t.Fatalf("a CloseTimedOut return must stay reported as a stop timeout, got %q", resp.GetError())
	}
	if currentBox() != nil {
		t.Fatal("the stop must have unpublished the runtime")
	}
	if pendingTeardown() != nil {
		t.Fatal("a close that already completed must not stay registered in the teardown record (REC-02C)")
	}

	// The stub skipped the first runtime's real close: close it FOR REAL,
	// through the standard close function, before the next Start — the
	// raced completion cleared the record, but the runtime itself must not
	// leak into the rest of the test.
	if capturedBox == nil {
		t.Fatal("the modeled stop did not hand over the captured runtime")
	}
	capturedClosed = true
	if outcome, closeErr := capturedBox.CloseWithTimeout(capturedCancel, 2*time.Second, log.Println, nil); outcome != boxbox.CloseCompleted {
		t.Fatalf("the captured first runtime must close for real before the next start, got outcome %v (err %v)", outcome, closeErr)
	}

	// The point of the fix: the next Start needs no extra Stop to release
	// the lock — the raced completion already cleared the record.
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("the start after the raced completion must not need an extra stop first: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("no runtime after the post-race start")
	}
	// The final stop must be clean on BOTH channels: no transport error and
	// no error inside the typed payload.
	stopResp, stopErr := globalServer.Stop(context.Background(), &gen.EmptyReq{})
	if stopErr != nil {
		t.Fatalf("the final stop must not carry a Go error, got %v", stopErr)
	}
	if stopResp.GetError() != "" {
		t.Fatalf("the final stop must not carry an ErrorResp error, got %q", stopResp.GetError())
	}
	if currentBox() != nil {
		t.Fatal("runtime left after the clean stop")
	}
	if pendingTeardown() != nil {
		t.Fatal("teardown record left after the clean stop")
	}
}
