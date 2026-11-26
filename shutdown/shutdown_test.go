package shutdown

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetShutdownForTest restores the once-per-process shutdown state and
// replaces the real sequence (which needs a GRO runtime) with body.
func resetShutdownForTest(t *testing.T, body func() bool) {
	t.Helper()
	prev := shutdownBody
	reset := func() {
		inProgress.Store(false)
		shutdownOnce = sync.Once{}
		shutdownOK = false
		SetDeadline(time.Time{})
	}
	reset()
	shutdownBody = body
	t.Cleanup(func() {
		shutdownBody = prev
		reset()
	})
}

func TestShutdown_MarksInProgressBeforeRunning(t *testing.T) {
	sawInProgress := false
	resetShutdownForTest(t, func() bool { sawInProgress = InProgress(); return true })

	if InProgress() {
		t.Fatal("InProgress before any shutdown")
	}
	if !Shutdown() {
		t.Fatal("Shutdown result lost")
	}
	if !sawInProgress {
		t.Fatal("InProgress must already be true while the sequence runs")
	}
	if !InProgress() {
		t.Fatal("InProgress must stay true after Shutdown")
	}
}

// Review PP-d694e9 follow-up: a signal arriving during a CLI stopService must
// not run the GRO and logger shutdown a second time.
func TestShutdown_RunsOnceAndSharesResult(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	resetShutdownForTest(t, func() bool {
		calls.Add(1)
		<-release
		return false
	})

	results := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		go func() { results <- Shutdown() }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	for i := 0; i < 3; i++ {
		if <-results {
			t.Fatal("every caller must get the first run's result (false)")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("shutdown sequence ran %d times, want 1", n)
	}
}

func TestShutdownAndExitInBackground(t *testing.T) {
	codes := captureExit(t)
	release := make(chan struct{})
	resetShutdownForTest(t, func() bool { <-release; return true })

	ShutdownAndExitInBackground()
	// Must return at once with the shutdown already marked, so the caller's
	// goroutine can return and main sees InProgress when the CLI ends.
	if !InProgress() {
		t.Fatal("InProgress must be true when ShutdownAndExitInBackground returns")
	}
	if effectiveBudget(time.Hour) > backgroundShutdownBudget {
		t.Fatal("background shutdown must set a closer deadline")
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		exitMu.Lock()
		n := len(*codes)
		exitMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	exitMu.Lock()
	defer exitMu.Unlock()
	if len(*codes) != 1 || (*codes)[0] != 0 {
		t.Fatalf("exit codes = %v, want [0]", *codes)
	}
}
