package proposalguard

import (
	"sync"
	"testing"
	"time"
)

// The core block-858 property: a second candidate for a height already in flight is
// refused, and the height becomes claimable again only after Release.
func TestClaimRejectsSecondInFlight(t *testing.T) {
	g := New(time.Minute)

	if !g.Claim(858) {
		t.Fatal("first claim of height 858 must succeed")
	}
	if g.Claim(858) {
		t.Fatal("second claim of an in-flight height 858 must be REFUSED (this is the block-858 dup)")
	}
	// A different height is independent.
	if !g.Claim(859) {
		t.Fatal("claim of a different height 859 must succeed")
	}

	g.Release(858)
	if !g.Claim(858) {
		t.Fatal("after Release, height 858 must be claimable again (legit retry after a failed round)")
	}
}

// A claim that is never released must expire after its TTL so a dead consensus round
// cannot wedge the height forever.
func TestClaimExpiresAfterTTL(t *testing.T) {
	g := New(20 * time.Millisecond)
	if !g.Claim(100) {
		t.Fatal("first claim must succeed")
	}
	if g.Claim(100) {
		t.Fatal("immediate re-claim must be refused")
	}
	time.Sleep(40 * time.Millisecond)
	if !g.Claim(100) {
		t.Fatal("after TTL expiry the height must be reclaimable")
	}
}

// Release of an unclaimed height is a no-op (validators never claim).
func TestReleaseUnclaimedIsNoop(t *testing.T) {
	g := New(time.Minute)
	g.Release(777) // must not panic
	if g.InFlight(777) {
		t.Fatal("unclaimed height must not be in-flight")
	}
}

// Under concurrent claims for the same height, exactly one wins.
func TestConcurrentClaimExactlyOneWins(t *testing.T) {
	g := New(time.Minute)
	const n = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if g.Claim(42) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one concurrent claim must win, got %d", wins)
	}
}
