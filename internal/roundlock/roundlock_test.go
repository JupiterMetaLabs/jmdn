package roundlock

import (
	"sync"
	"testing"
)

func TestTryLock_BlockThenTimeoutIsRefused(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 848, Period: 0}
	if ok, _ := l.TryLock(r, Block); !ok {
		t.Fatalf("first signature for a round must be allowed")
	}
	ok, taken := l.TryLock(r, Timeout)
	if ok || taken != Block {
		t.Fatalf("timeout after block for the same round must be refused, got ok=%v taken=%v", ok, taken)
	}
}

func TestTryLock_TimeoutThenBlockIsRefused(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 848, Period: 2}
	if ok, _ := l.TryLock(r, Timeout); !ok {
		t.Fatalf("first signature for a round must be allowed")
	}
	ok, taken := l.TryLock(r, Block)
	if ok || taken != Timeout {
		t.Fatalf("block after timeout for the same round must be refused, got ok=%v taken=%v", ok, taken)
	}
}

func TestTryLock_SameSideIsIdempotent(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 1}
	for i := 0; i < 3; i++ {
		if ok, _ := l.TryLock(r, Block); !ok {
			t.Fatalf("retrying the side already taken must stay allowed (attempt %d)", i)
		}
	}
}

func TestTryLock_DifferentRoundsAreIndependent(t *testing.T) {
	l := NewLedger()
	if ok, _ := l.TryLock(Round{Height: 848, Period: 0}, Timeout); !ok {
		t.Fatal("lock p0")
	}
	// Timing out period 0 must not stop signing the block at period 1.
	if ok, _ := l.TryLock(Round{Height: 848, Period: 1}, Block); !ok {
		t.Fatalf("a block result for the NEXT period must be allowed after timing out the previous one")
	}
	if ok, _ := l.TryLock(Round{Height: 849, Period: 0}, Block); !ok {
		t.Fatalf("a different height must be independent")
	}
}

func TestTryLock_ConcurrentOppositeSidesExactlyOneWins(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		l := NewLedger()
		r := Round{Height: uint64(trial), Period: 0}
		var wg sync.WaitGroup
		results := make([]bool, 2)
		for i, side := range []Side{Block, Timeout} {
			wg.Add(1)
			go func(i int, s Side) {
				defer wg.Done()
				results[i], _ = l.TryLock(r, s)
			}(i, side)
		}
		wg.Wait()
		if results[0] == results[1] {
			t.Fatalf("trial %d: exactly one side must win, got block=%v timeout=%v", trial, results[0], results[1])
		}
	}
}

func TestTryLock_PrunesOldRoundsButKeepsRecent(t *testing.T) {
	l := NewLedger()
	l.TryLock(Round{Height: 1, Period: 0}, Block)
	l.TryLock(Round{Height: 5000, Period: 0}, Block)
	if _, ok := l.Signed(Round{Height: 1, Period: 0}); ok {
		t.Fatalf("a round far below the tip should be pruned")
	}
	l.TryLock(Round{Height: 5000 - retainHeights + 1, Period: 0}, Timeout)
	if _, ok := l.Signed(Round{Height: 5000 - retainHeights + 1, Period: 0}); !ok {
		t.Fatalf("a round inside the retention window must be kept")
	}
}
