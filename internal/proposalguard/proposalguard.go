// Package proposalguard is the sequencer's per-height admission gate: at most one
// block candidate per height may be in consensus at a time.
//
// # WHY THIS EXISTS (block-858, 2026-09-25)
//
// The sequencer accepted TWO different candidates for height 858. The orchestrator
// polls /api/latest-block to compute the next height; while the first 858 candidate
// was still in consensus the poll still returned 857, so the orchestrator generated
// "857+1" a second time with a different transaction set. Both candidates reached
// quorum; the second applied its transactions and then failed to store, corrupting
// state. The ingress had no guard against a second candidate for a height that was
// committed or already in flight.
//
// This guard is the sequencer-side half of the fix (the orchestrator not
// re-proposing an in-flight height is the other half). Block/Server.go claims a
// height before starting consensus and rejects (HTTP 409) a second claim; the
// consensus terminal (ProcessBlockLocally) releases it.
//
// # WHY NOT roundlock
//
// internal/roundlock is a per-node mutual-exclusion ledger between BLOCK and
// TIMEOUT signatures for a consensus round, keyed by (height, period), with
// release-on-sign-failure and idempotent-re-entry semantics tuned for that safety
// property. It is the wrong primitive for an ingress admission gate: it is
// period-scoped (the ingress does not own a period yet), and its release rules are
// about signing, not about "a proposal for this height is being processed". Reusing
// it would entangle two unrelated invariants. This is a separate, purpose-built
// guard, as the block-858 handover asked us to confirm.
//
// # TTL
//
// consensus.Start is asynchronous — it hands the round to a goroutine and returns
// before the block commits — so a claim cannot be released purely on the ingress
// handler's return. The consensus terminal releases it explicitly, but a round that
// dies without reaching that terminal (total timeout, crash) must not wedge the
// height forever. Every claim therefore carries a TTL; an expired claim is
// reclaimable. The TTL only needs to exceed one consensus round's wall-clock.
package proposalguard

import (
	"sync"
	"time"
)

// Guard tracks in-flight heights with per-claim expiry.
type Guard struct {
	mu       sync.Mutex
	inflight map[uint64]time.Time // height -> claim expiry (absolute)
	ttl      time.Duration
}

// New returns a Guard whose claims expire after ttl.
func New(ttl time.Duration) *Guard {
	return &Guard{inflight: make(map[uint64]time.Time), ttl: ttl}
}

// DefaultTTL bounds how long a claim survives without an explicit Release. It must
// comfortably exceed one consensus round (observed ~60-90s on the 30-validator
// testnet); 3 minutes leaves margin while keeping a dead round's height reclaimable.
const DefaultTTL = 3 * time.Minute

// Default is the process-wide guard the sequencer ingress uses.
var Default = New(DefaultTTL)

// Claim records height as in-flight and returns true, unless an UNEXPIRED claim for
// the same height already exists — in which case it returns false and the caller
// must reject the proposal. Expired claims (including for other heights, pruned
// opportunistically) do not block a new claim.
func (g *Guard) Claim(height uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if exp, ok := g.inflight[height]; ok && now.Before(exp) {
		return false
	}
	g.inflight[height] = now.Add(g.ttl)
	// Opportunistic prune so the map cannot grow without bound if some heights
	// never get an explicit Release (TTL-only cleanup).
	for h, e := range g.inflight {
		if h != height && !now.Before(e) {
			delete(g.inflight, h)
		}
	}
	return true
}

// Release clears the claim for height. It is safe and idempotent: releasing an
// unclaimed height (e.g. on a node that never claimed it) is a no-op.
func (g *Guard) Release(height uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.inflight, height)
}

// InFlight reports whether height currently holds an unexpired claim.
func (g *Guard) InFlight(height uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.inflight[height]
	return ok && time.Now().Before(exp)
}
