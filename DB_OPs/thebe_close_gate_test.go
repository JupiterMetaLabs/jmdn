package DB_OPs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Review A1: ThebeDB must not be closed while a block apply / rollback holds
// the state-apply lock.
func TestCloseGated_SkipsWhileApplyInFlight(t *testing.T) {
	closed := false
	err := closeGated(
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
	err := closeGated(
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
	err := closeGated(
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
	if err := closeGated(func(time.Duration) bool { return true }, nil, func() error { closed = true; return nil }, time.Second); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("not closed")
	}
}

// The append-slot wait shares the gate's single time budget.
func TestCloseGated_SlotWaitBoundedByGateBudget(t *testing.T) {
	start := time.Now()
	_ = closeGated(
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
	err := closeGated(TryLockStateApplyWithin, nil, func() error { closed = true; return nil }, 50*time.Millisecond)
	if !errors.Is(err, ErrThebeCloseSkipped) || closed {
		t.Fatalf("err=%v closed=%v: must skip while the apply holds the lock", err, closed)
	}
	UnlockStateApply() // the apply finishes

	if err := closeGated(TryLockStateApplyWithin, nil, func() error { closed = true; return nil }, time.Second); err != nil || !closed {
		t.Fatalf("err=%v closed=%v: must close once the lock is free", err, closed)
	}
	// The gate keeps the lock: a new apply cannot start after the close.
	if TryLockStateApplyWithin(20 * time.Millisecond) {
		t.Fatal("an apply could start after the close")
	}
	UnlockStateApply() // test cleanup: release the lock the gate kept
}
