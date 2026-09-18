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
	if currentBox() == nil {
		t.Fatal("the first start did not publish the runtime")
	}

	// The stub skips the real close (like the failed-close stub in REC-02B):
	// the completion callback runs inline, then the bounded wait reports the
	// timeout — exactly the budget-boundary interleaving.
	var closedBox *boxbox.Box
	var closedCancel context.CancelFunc
	prevClose := closePublishedRuntime
	closePublishedRuntime = func(box *boxbox.Box, cancel context.CancelFunc, d time.Duration, onDone func(error)) (boxbox.CloseOutcome, error) {
		closedBox, closedCancel = box, cancel
		onDone(nil)
		return boxbox.CloseTimedOut, nil
	}
	t.Cleanup(func() {
		closePublishedRuntime = prevClose
		// The stub skipped the real close: close the captured runtime for
		// real, then stop whatever instance the test left published.
		if closedBox != nil {
			_, _ = closedBox.CloseWithTimeout(closedCancel, 2*time.Second, log.Println, nil)
		}
		if currentBox() != nil {
			_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		}
		autoRedirectMark.Store(0)
	})

	resp, err := globalServer.Stop(context.Background(), &gen.EmptyReq{})
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

	// The point of the fix: the next Start needs no extra Stop to release
	// the lock — the raced completion already cleared the record.
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("the start after the raced completion must not need an extra stop first: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("no runtime after the post-race start")
	}
	if _, err := globalServer.Stop(context.Background(), &gen.EmptyReq{}); err != nil {
		t.Fatalf("the stop after the post-race start must be clean, got %v", err)
	}
	if currentBox() != nil {
		t.Fatal("runtime left after the clean stop")
	}
}
