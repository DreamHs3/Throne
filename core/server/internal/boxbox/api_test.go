package boxbox

import (
	"fmt"
	"testing"
	"time"
)

// REC-02 R2: CloseWithTimeout must give up at the deadline even while the
// close is still running - the previous implementation logged the warning
// and then waited for the close unconditionally, which made every "wait"
// caller unbounded (the stop path hung with the lifecycle lock held).
func TestRunCloseBoundedReturnsPastDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	closer := &blockingCloser{entered: entered, release: release, closed: closed}

	done := make(chan struct{})
	logs := make(chan string, 4)
	go func() {
		runCloseBounded(closer.close, 200*time.Millisecond, func(v ...any) {
			logs <- fmt.Sprint(v...)
		})
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the close was never started")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runCloseBounded did not return past the deadline")
	}

	// The deadline warning must have been logged.
	select {
	case msg := <-logs:
		if want := "takes longer than expected"; !contains(msg, want) {
			t.Fatalf("deadline log %q must mention %q", msg, want)
		}
	default:
		t.Fatal("the deadline warning was not logged")
	}

	// The close keeps running in the background until released; release it
	// and give the goroutine time to finish so the test does not leak it.
	close(release)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the background close never finished after release")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type blockingCloser struct {
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (b *blockingCloser) close() {
	close(b.entered)
	<-b.release
	close(b.closed)
}
