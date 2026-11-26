package DB_OPs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gateFree stands in for a block-apply gate nobody holds.
func gateFree(time.Duration) bool { return true }

// releaseBlockApplyGateForTest undoes a gate taken by a test that closed.
func releaseBlockApplyGateForTest() { blockApplyGate.Unlock() }

// Review A1: ThebeDB must not be closed while a block apply / rollback holds
// the state-apply lock.
func TestCloseGated_SkipsWhileApplyInFlight(t *testing.T) {
	closed := false
	err := closeGated(gateFree,
		func(time.Duration) bool { return false }, // an apply holds the lock
		func(context.Context) error { return nil },
		func() error { closed = true; return nil },
		50*time.Millisecond,
	)
	if !errors.Is(err, ErrThebeCloseSkipped) {
		t.Fatalf("err = %v, want ErrThebeCloseSkipped", err)
	}
	if closed {
		t.Fatal("ThebeDB was closed while an apply held the state-apply lock")
	}
}

// Review A1: ThebeDB must not be closed while a 2PC append holds the slot.
func TestCloseGated_SkipsWhileAppendInFlight(t *testing.T) {
	closed := false
	err := closeGated(gateFree,
		func(time.Duration) bool { return true },
		func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, // slot busy
		func() error { closed = true; return nil },
		50*time.Millisecond,
	)
	if !errors.Is(err, ErrThebeCloseSkipped) {
		t.Fatalf("err = %v, want ErrThebeCloseSkipped", err)
	}
	if closed {
		t.Fatal("ThebeDB was closed while an append held the slot")
	}
}

func TestCloseGated_ClosesWhenIdle(t *testing.T) {
	order := []string{}
	err := closeGated(gateFree,
		func(time.Duration) bool { order = append(order, "lock"); return true },
		func(context.Context) error { order = append(order, "slot"); return nil },
		func() error { order = append(order, "close"); return nil },
		time.Second,
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(order) != 3 || order[0] != "lock" || order[1] != "slot" || order[2] != "close" {
		t.Fatalf("order = %v, want [lock slot close]", order)
	}
}

func TestCloseGated_NoAppendSlot(t *testing.T) {
	closed := false
	if err := closeGated(gateFree, func(time.Duration) bool { return true }, nil, func() error { closed = true; return nil }, time.Second); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("not closed")
	}
}

// The append-slot wait shares the gate's single time budget.
func TestCloseGated_SlotWaitBoundedByGateBudget(t *testing.T) {
	start := time.Now()
	_ = closeGated(gateFree,
		func(time.Duration) bool { return true },
		func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		func() error { return nil },
		100*time.Millisecond,
	)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("gate waited %v for a 100ms budget", took)
	}
}

func TestTryLockStateApplyWithin(t *testing.T) {
	LockStateApply()
	start := time.Now()
	if TryLockStateApplyWithin(50 * time.Millisecond) {
		t.Fatal("got the lock while it was held")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("TryLock waited %v for a 50ms limit", took)
	}
	UnlockStateApply()

	if !TryLockStateApplyWithin(50 * time.Millisecond) {
		t.Fatal("did not get a free lock")
	}
	UnlockStateApply()
}

// With the real state-apply lock: an apply holding it keeps ThebeDB open, and
// once the gate has the lock, a later apply cannot start.
func TestCloseGated_RealLock(t *testing.T) {
	LockStateApply() // an apply in progress
	closed := false
	err := closeGated(gateFree, TryLockStateApplyWithin, nil, func() error { closed = true; return nil }, 50*time.Millisecond)
	if !errors.Is(err, ErrThebeCloseSkipped) || closed {
		t.Fatalf("err=%v closed=%v: must skip while the apply holds the lock", err, closed)
	}
	UnlockStateApply() // the apply finishes

	if err := closeGated(gateFree, TryLockStateApplyWithin, nil, func() error { closed = true; return nil }, time.Second); err != nil || !closed {
		t.Fatalf("err=%v closed=%v: must close once the lock is free", err, closed)
	}
	// The gate keeps the lock: a new apply cannot start after the close.
	if TryLockStateApplyWithin(20 * time.Millisecond) {
		t.Fatal("an apply could start after the close")
	}
	UnlockStateApply() // test cleanup: release the lock the gate kept
}

// Review A1: a block apply in progress keeps ThebeDB open; once the gate is
// taken, a new block apply cannot start.
func TestCloseGated_SkipsWhileBlockApplyRunning(t *testing.T) {
	EnterBlockApply() // a block apply is running
	closed := false
	err := closeGated(tryCloseBlockApplyWithin, gateFree, nil, func() error { closed = true; return nil }, 50*time.Millisecond)
	if !errors.Is(err, ErrThebeCloseSkipped) || closed {
		t.Fatalf("err=%v closed=%v: must skip while a block apply runs", err, closed)
	}
	ExitBlockApply()

	if err := closeGated(tryCloseBlockApplyWithin, gateFree, nil, func() error { closed = true; return nil }, time.Second); err != nil || !closed {
		t.Fatalf("err=%v closed=%v: must close once no block apply runs", err, closed)
	}
	started := make(chan struct{})
	go func() { EnterBlockApply(); close(started); ExitBlockApply() }()
	select {
	case <-started:
		t.Fatal("a block apply started after ThebeDB was closed")
	case <-time.After(50 * time.Millisecond):
	}
	releaseBlockApplyGateForTest()
	<-started
}

// Review A1 verification ("SIGTERM while a rollback is in progress"): the
// rollback releases the state-apply lock and only then writes the apply-halt
// record (rollbackAbandoned). A shutdown that arrives in that gap must not
// close ThebeDB before the halt record is written. Uses the real gate and
// lock, in the order block apply takes them.
func TestCloseGated_StopDuringRollbackKeepsHaltRecord(t *testing.T) {
	var mu sync.Mutex
	haltWritten := false
	closedBeforeHalt := false

	gapReached := make(chan struct{})
	finishRollback := make(chan struct{})
	applyDone := make(chan struct{})
	go func() { // a block apply whose rollback is abandoned
		defer close(applyDone)
		EnterBlockApply()
		defer ExitBlockApply()
		LockStateApply()
		// ... partial restore under the state-apply lock ...
		UnlockStateApply()
		close(gapReached) // the gap: lock released, halt not yet written
		<-finishRollback
		mu.Lock()
		haltWritten = true // haltApply -> RecordApplyHalt
		mu.Unlock()
	}()

	<-gapReached
	closeErr := make(chan error, 1)
	go func() { // SIGTERM lands in the gap: the ThebeDB closer runs
		closeErr <- closeGated(tryCloseBlockApplyWithin, TryLockStateApplyWithin, nil, func() error {
			mu.Lock()
			closedBeforeHalt = !haltWritten
			mu.Unlock()
			return nil
		}, 2*time.Second)
	}()
	time.Sleep(100 * time.Millisecond) // the closer is now waiting on the gate
	close(finishRollback)
	<-applyDone

	if err := <-closeErr; err != nil {
		t.Fatalf("closer: %v", err)
	}
	if closedBeforeHalt {
		t.Fatal("ThebeDB was closed before the apply-halt record was written")
	}
	releaseBlockApplyGateForTest()
	UnlockStateApply()
}
