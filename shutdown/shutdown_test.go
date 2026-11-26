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
	if effectiveBudget(time.Hour) > watchdogBudget-2*time.Second {
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

// Every shutdown path cancels main's context before the sequence runs, not
// only the signal handler (review open hypothesis on stopService).
func TestShutdown_CancelsMainContextFirst(t *testing.T) {
	cancelled := false
	sawCancelled := false
	resetShutdownForTest(t, func() bool { sawCancelled = cancelled; return true })
	SetMainCancel(func() { cancelled = true })
	t.Cleanup(func() { SetMainCancel(nil) })

	Shutdown()
	if !sawCancelled {
		t.Fatal("main's context must be cancelled before the shutdown sequence runs")
	}
}

// shortWatchdogForTest shortens the ShutdownAndExit watchdog.
func shortWatchdogForTest(t *testing.T, d time.Duration) {
	t.Helper()
	prev := watchdogBudget
	watchdogBudget = d
	t.Cleanup(func() { watchdogBudget = prev })
}

func waitForExitCodes(t *testing.T, codes *[]int, n int, within time.Duration) []int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		exitMu.Lock()
		got := append([]int(nil), (*codes)...)
		exitMu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Review open hypothesis: stopService and the CLI-failure path had no
// watchdog. A shutdown step that blocks must not keep the process running.
func TestShutdownAndExit_WatchdogExitsWhenShutdownBlocks(t *testing.T) {
	codes := captureExit(t)
	block := make(chan struct{})
	resetShutdownForTest(t, func() bool { <-block; return true })
	shortWatchdogForTest(t, 100*time.Millisecond)

	finished := make(chan struct{})
	go func() { ShutdownAndExit(false); close(finished) }()
	got := waitForExitCodes(t, codes, 1, 2*time.Second)

	// In production the watchdog's exit ends the process here. The test stub
	// does not, so release the blocked shutdown and wait for it before the
	// cleanups reset the package state it is still using.
	close(block)
	<-finished

	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("exit codes = %v, want [1] from the watchdog", got)
	}
}

func TestShutdownAndExit_ExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		clean  bool
		failed bool
		want   int
	}{
		{"clean", true, false, 0},
		{"unclean shutdown", false, false, 1},
		{"caller failed", true, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codes := captureExit(t)
			clean := tc.clean
			resetShutdownForTest(t, func() bool { return clean })
			shortWatchdogForTest(t, 200*time.Millisecond)

			ShutdownAndExit(tc.failed)
			// Wait past the watchdog: a stopped watchdog must not add a second exit.
			time.Sleep(300 * time.Millisecond)
			exitMu.Lock()
			got := append([]int(nil), (*codes)...)
			exitMu.Unlock()
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("exit codes = %v, want [%d]", got, tc.want)
			}
		})
	}
}
