package boxbox

import (
	"context"
	"fmt"
	"time"
)

// REC-02 R2: CloseWithTimeout gives up after d. The previous version logged
// the deadline warning and then waited unconditionally for the close, so
// every caller that asked to wait was unbounded in practice - a hung close
// held the lifecycle lock and, through it, the service stop path hostage.
// The close itself is never abandoned: if it outlives the deadline it keeps
// running in the background and prints its own completion time.
func (s *Box) CloseWithTimeout(cancel context.CancelFunc, d time.Duration, logFunc func(v ...any)) {
	runCloseBounded(func() {
		cancel()
		_ = s.Close()
	}, d, logFunc)
}

// runCloseBounded runs closeFn on its own goroutine and returns when the
// close finishes or d elapses, whichever comes first.
func runCloseBounded(closeFn func(), d time.Duration, logFunc func(v ...any)) {
	start := time.Now()
	t := time.NewTimer(d)
	done := make(chan struct{})

	printCloseTime := func() {
		logFunc("[Info] sing-box closed in", fmt.Sprintf("%d ms", time.Since(start).Milliseconds()))
	}

	go func() {
		closeFn()
		close(done)
		if !t.Stop() {
			printCloseTime()
		}
	}()

	select {
	case <-t.C:
		logFunc("[Warning] sing-box close takes longer than expected; it continues in the background")
	case <-done:
		printCloseTime()
	}
}
