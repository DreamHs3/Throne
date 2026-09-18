package boxbox

import (
	"context"
	"fmt"
	"time"
)

// REC-02B: the outcome of a bounded close is explicit. CloseCompleted and
// CloseFailed both mean the close FINISHED within the budget (CloseFailed
// carries its error); CloseTimedOut means the WAIT gave up while the close is
// still running in the background. A caller must never treat CloseTimedOut as
// a completed teardown: the runtime resources are still held until the close
// actually finishes.
type CloseOutcome int

const (
	CloseCompleted CloseOutcome = iota
	CloseFailed
	CloseTimedOut
)

// CloseWithTimeout closes the box and gives up the WAIT after d; the close
// itself is never abandoned - if it outlives the deadline it keeps running in
// the background and prints its own completion time (REC-02 R2). The outcome
// is explicit so the stop path can tell a finished close (completed/failed)
// from a close that outlived the budget (timed out) and report accordingly.
//
// onDone, when non-nil, runs exactly once on the close goroutine AFTER the
// close finishes - however late - with the close error. A caller that handed
// the closing runtime to a background ownership record uses it to reclaim the
// resources at the real completion.
func (s *Box) CloseWithTimeout(cancel context.CancelFunc, d time.Duration, logFunc func(v ...any), onDone func(error)) (CloseOutcome, error) {
	completed, closeErr := runCloseBounded(func() error {
		cancel()
		return s.Close()
	}, d, logFunc, onDone)
	if !completed {
		return CloseTimedOut, nil
	}
	if closeErr != nil {
		return CloseFailed, closeErr
	}
	return CloseCompleted, nil
}

// runCloseBounded runs closeFn on its own goroutine and returns when the
// close finishes (with its error) or d elapses, whichever comes first. A
// close that finishes after the return is reported only through onDone.
func runCloseBounded(closeFn func() error, d time.Duration, logFunc func(v ...any), onDone func(error)) (completed bool, closeErr error) {
	start := time.Now()
	t := time.NewTimer(d)
	done := make(chan error, 1)

	printCloseTime := func() {
		logFunc("[Info] sing-box closed in", fmt.Sprintf("%d ms", time.Since(start).Milliseconds()))
	}

	go func() {
		err := closeFn()
		done <- err
		if !t.Stop() {
			printCloseTime()
		}
		if onDone != nil {
			onDone(err)
		}
	}()

	select {
	case <-t.C:
		logFunc("[Warning] sing-box close takes longer than expected; it continues in the background")
		return false, nil
	case err := <-done:
		printCloseTime()
		return true, err
	}
}
