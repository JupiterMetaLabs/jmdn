package roundlock

import "testing"

func withRequireDurable(t *testing.T, on bool) {
	t.Helper()
	prev := requireDurable.Load()
	RequireDurableStore(on)
	t.Cleanup(func() { RequireDurableStore(prev) })
}

// With the requirement on, a ledger without a store refuses to sign and
// records nothing: a restart can never make it sign a second block.
func TestRequireDurable_NoStoreRefuses(t *testing.T) {
	withRequireDurable(t, true)
	l := NewLedger()
	ok, refusal, _, fresh := l.TryLockFresh(Round{Height: 10, Period: 0}, "A")
	if ok || fresh || refusal != Unavailable {
		t.Fatalf("got ok=%v fresh=%v refusal=%v; want refused Unavailable", ok, fresh, refusal)
	}
	if _, signed := l.Signed(Round{Height: 10, Period: 0}); signed {
		t.Fatal("a refused reservation must not be recorded")
	}
	if refusal.String() != "ledger_unavailable" {
		t.Fatalf("refusal string %q", refusal.String())
	}
}

// With a store the requirement changes nothing.
func TestRequireDurable_WithStoreSigns(t *testing.T) {
	withRequireDurable(t, true)
	l := durable(newMemStore())
	if !l.Durable() {
		t.Fatal("ledger with a store must report Durable")
	}
	if ok, _, _, fresh := l.TryLockFresh(Round{Height: 10, Period: 0}, "A"); !ok || !fresh {
		t.Fatalf("durable ledger refused: ok=%v fresh=%v", ok, fresh)
	}
	if ok, refusal, _, _ := l.TryLockFresh(Round{Height: 10, Period: 0}, "B"); ok || refusal != Conflict {
		t.Fatalf("R1 still applies: ok=%v refusal=%v", ok, refusal)
	}
}

// Off (the default for tests and tools): in-memory ledgers work as before.
func TestRequireDurable_OffKeepsInMemoryBehaviour(t *testing.T) {
	withRequireDurable(t, false)
	l := NewLedger()
	if l.Durable() {
		t.Fatal("ledger without a store must not report Durable")
	}
	if ok, _, _, fresh := l.TryLockFresh(Round{Height: 10, Period: 0}, "A"); !ok || !fresh {
		t.Fatalf("in-memory ledger refused: ok=%v fresh=%v", ok, fresh)
	}
}

// A committed height is still reported as Committed, not Unavailable.
func TestRequireDurable_CommittedHeightKeepsItsReason(t *testing.T) {
	withRequireDurable(t, true)
	l := NewLedger()
	l.ForgetThrough(20)
	if ok, refusal, _, _ := l.TryLockFresh(Round{Height: 20, Period: 0}, "A"); ok || refusal != Committed {
		t.Fatalf("got ok=%v refusal=%v; want Committed", ok, refusal)
	}
}
