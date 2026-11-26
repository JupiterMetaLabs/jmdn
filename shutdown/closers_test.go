package shutdown

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// resetClosersForTest clears the registry and any deadline.
func resetClosersForTest() {
	closersMu.Lock()
	closers = nil
	closersMu.Unlock()
	SetDeadline(time.Time{})
}

func TestRunClosers_ReverseOrder(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	var mu sync.Mutex
	var got []string
	rec := func(name string) func() error {
		return func() error {
			mu.Lock()
			got = append(got, name)
			mu.Unlock()
			return nil
		}
	}
	// Registered in creation order, as main.go does.
	Register("pools", time.Second, rec("pools"))
	Register("thebedb", time.Second, rec("thebedb"))
	RegisterFunc("outbox", time.Second, func() { _ = rec("outbox")() })
	Register("host", time.Second, rec("host"))

	if !runClosers(5 * time.Second) {
		t.Fatal("expected all closers to succeed")
	}
	want := []string{"host", "outbox", "thebedb", "pools"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRunClosers_EachRunsOnce(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	calls := 0
	Register("db", time.Second, func() error { calls++; return nil })

	runClosers(time.Second)
	runClosers(time.Second) // e.g. Shutdown() and then main's deferred RunClosers
	if calls != 1 {
		t.Fatalf("closer ran %d times, want 1", calls)
	}
}

func TestRunClosers_StuckCloserDoesNotBlock(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	ranAfter := false
	Register("first-registered", time.Second, func() error { ranAfter = true; return nil })
	Register("stuck", 100*time.Millisecond, func() error { <-block; return nil })

	start := time.Now()
	ok := runClosers(5 * time.Second)
	took := time.Since(start)

	if ok {
		t.Fatal("expected false: one closer timed out")
	}
	if took > time.Second {
		t.Fatalf("stuck closer blocked shutdown for %v", took)
	}
	if !ranAfter {
		t.Fatal("closers after the stuck one must still run")
	}
}

func TestRunClosers_BudgetCapsTotalTime(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	// Three closers that each would take their full 1s; the budget is 300ms.
	for _, n := range []string{"a", "b", "c"} {
		Register(n, time.Second, func() error { <-block; return nil })
	}
	start := time.Now()
	if runClosers(300 * time.Millisecond) {
		t.Fatal("expected false")
	}
	if took := time.Since(start); took > 800*time.Millisecond {
		t.Fatalf("budget not enforced: took %v", took)
	}
}

func TestRunClosers_ErrorAndPanicReportFalse(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	Register("err", time.Second, func() error { return errors.New("boom") })
	if runClosers(time.Second) {
		t.Fatal("error must report false")
	}

	resetClosersForTest()
	Register("panic", time.Second, func() error { panic("bad close") })
	if runClosers(time.Second) {
		t.Fatal("panic must report false")
	}
}

func TestRegister_IgnoresNil(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	Register("nil", time.Second, nil)
	RegisterFunc("nil", time.Second, nil)
	if !runClosers(time.Second) {
		t.Fatal("no closers registered: expected true")
	}
}

// exitMu guards the slice captureExit records into; exit may be called from
// another goroutine.
var exitMu sync.Mutex

func captureExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	prev := exitFn
	exitFn = func(c int) {
		exitMu.Lock()
		codes = append(codes, c)
		exitMu.Unlock()
	}
	t.Cleanup(func() { exitFn = prev })
	return &codes
}

func TestExit_AlwaysExits(t *testing.T) {
	codes := captureExit(t)
	Exit(true)
	Exit(false)
	if len(*codes) != 2 || (*codes)[0] != 0 || (*codes)[1] != 1 {
		t.Fatalf("exit codes = %v, want [0 1]", *codes)
	}
}

func TestForceExitOnSignal_SecondSignalExits(t *testing.T) {
	codes := captureExit(t)
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() { ForceExitOnSignal(sigCh); close(done) }()

	sigCh <- syscall.SIGTERM
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second signal did not trigger exit")
	}
	if len(*codes) != 1 || (*codes)[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", *codes)
	}
}

func TestForceExitOnSignal_ClosedChannelDoesNotExit(t *testing.T) {
	codes := captureExit(t)
	sigCh := make(chan os.Signal)
	close(sigCh)
	ForceExitOnSignal(sigCh)
	if len(*codes) != 0 {
		t.Fatalf("closed channel must not exit, got %v", *codes)
	}
}

// Review PP-61ae8a: the closers must finish before main's watchdog, not just
// within their own budget. A deadline closer than the budget shortens it.
func TestRunClosers_DeadlineCapsBudget(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	skippedRan := false
	Register("thebedb", 5*time.Second, func() error { skippedRan = true; return nil })
	Register("stuck", 5*time.Second, func() error { <-block; return nil })

	SetDeadline(time.Now().Add(200 * time.Millisecond))
	start := time.Now()
	if runClosers(10 * time.Second) {
		t.Fatal("expected false: the deadline cut the closers short")
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("deadline not honoured: took %v (budget 10s, deadline 200ms)", took)
	}
	if skippedRan {
		t.Fatal("a closer with no time left must be skipped, not started")
	}
}

func TestEffectiveBudget(t *testing.T) {
	resetClosersForTest()
	t.Cleanup(resetClosersForTest)

	if got := effectiveBudget(time.Second); got != time.Second {
		t.Fatalf("no deadline: %v, want 1s", got)
	}
	SetDeadline(time.Now().Add(time.Hour))
	if got := effectiveBudget(time.Second); got != time.Second {
		t.Fatalf("far deadline: %v, want 1s", got)
	}
	SetDeadline(time.Now().Add(-time.Second))
	if got := effectiveBudget(time.Second); got > 0 {
		t.Fatalf("past deadline: %v, want <= 0", got)
	}
}
