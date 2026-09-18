package boxbox

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// REC-02 R2: CloseWithTimeout must give up at the deadline even while the
// close is still running - the previous implementation logged the warning
// and then waited for the close unconditionally, which made every "wait"
// caller unbounded (the stop path hung with the lifecycle lock held).
// REC-02B: the give-up is reported as CloseTimedOut by CloseWithTimeout; at
// the runCloseBounded level it is (completed=false, err=nil).
func TestRunCloseBoundedReturnsPastDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	closer := &blockingCloser{entered: entered, release: release, closed: closed}

	done := make(chan struct{})
	logs := make(chan string, 4)
	go func() {
		completed, err := runCloseBounded(closer.close, 200*time.Millisecond, func(v ...any) {
			logs <- fmt.Sprint(v...)
		}, nil)
		if completed {
			t.Error("runCloseBounded must report an unfinished close as not completed")
		}
		if err != nil {
			t.Errorf("a timed-out close carries no error yet, got %v", err)
		}
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

// REC-02B: a close that finishes within the budget reports completion with
// its error, and the completion hook receives the same error exactly once.
func TestRunCloseBoundedReportsCompletedWithError(t *testing.T) {
	sentinel := errors.New("synthetic close failure")
	done := make(chan error, 1)
	hook := make(chan error, 1)
	go func() {
		_, closeErr := runCloseBounded(func() error { return sentinel }, time.Second,
			func(v ...any) {}, func(err error) { hook <- err })
		done <- closeErr
	}()

	select {
	case err := <-done:
		if !errors.Is(err, sentinel) {
			t.Fatalf("a failed close must carry its error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runCloseBounded did not return for a fast failing close")
	}
	select {
	case err := <-hook:
		if !errors.Is(err, sentinel) {
			t.Fatalf("the completion hook must receive the close error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the completion hook never ran for a fast failing close")
	}
}

// REC-02B: a close that finishes only AFTER the give-up reports nothing at
// the return (the caller already got the timeout) and delivers its result
// solely through the completion hook - however late. The stop path relies on
// this hook to clear its teardown-ownership record at the real completion.
func TestRunCloseBoundedDeliversLateCompletionThroughHook(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	hook := make(chan error, 1)
	sentinel := errors.New("late close error")

	returned := make(chan struct{})
	var completed bool
	var err error
	go func() {
		completed, err = runCloseBounded(func() error {
			close(entered)
			<-release
			return sentinel
		}, 100*time.Millisecond, func(v ...any) {}, func(hookErr error) { hook <- hookErr })
		close(returned)
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the close was never started")
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("runCloseBounded did not return past the deadline")
	}
	if completed || err != nil {
		t.Fatalf("the timed-out return must be (false, nil), got (%v, %v)", completed, err)
	}
	// Nothing may come through the hook before the close actually finishes.
	select {
	case err := <-hook:
		t.Fatalf("the completion hook ran before the close finished: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case hookErr := <-hook:
		if !errors.Is(hookErr, sentinel) {
			t.Fatalf("the late completion must deliver the close error, got %v", hookErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the completion hook never ran for the late close")
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

func (b *blockingCloser) close() error {
	close(b.entered)
	<-b.release
	close(b.closed)
	return nil
}
