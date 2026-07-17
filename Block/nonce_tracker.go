package Block

import (
	"strings"
	"sync"
	"time"
)

// PendingNonceTracker remembers the highest transaction nonce this node has
// successfully routed to the MRE, per sender, for a bounded time window.
//
// Purpose: cover the sequencing in-flight window for pending-nonce queries.
// After the orchestrator destructively pulls a tx from the mempool and before
// the block applies, the tx is observable nowhere (mempool ack protocol —
// tracker D-5 — is the systemic fix). For the dominant wallet flow (submit
// and query through the same node), this tracker bridges that gap.
//
// Design notes:
//   - Entries expire after TTL: if a tx neither mines nor returns to the pool
//     within the window, the tracker must stop inflating pending nonces (the
//     tx is likely rejected or orphaned — see chain nonce-jump semantics).
//   - Confirmed state always wins upward: NextFor never returns less than the
//     confirmed nonce passed in; stale tracker entries below it are ignored.
//   - Memory-bounded by opportunistic pruning on write.
//
// Note: the per-request SecurityCache in Security/ is NOT usable for this —
// it is created and closed inside each validation call (Security.go:81-82),
// so its optimistic nonce bump does not survive the request.
type PendingNonceTracker struct {
	mu      sync.RWMutex
	entries map[string]nonceEntry
	ttl     time.Duration
	now     func() time.Time // injectable clock for tests
}

type nonceEntry struct {
	highestNonce uint64
	recordedAt   time.Time
}

// defaultPendingNonceTTL bounds how long a submitted-but-unmined tx keeps
// advancing pending nonces. Generous vs the sequencing pipeline's worst case
// (orchestrator tick 120s + Espresso poll ≤60s + ZK ≤120s + apply).
const defaultPendingNonceTTL = 30 * time.Minute

// NewPendingNonceTracker builds a tracker with the given TTL (0 → default).
func NewPendingNonceTracker(ttl time.Duration) *PendingNonceTracker {
	if ttl <= 0 {
		ttl = defaultPendingNonceTTL
	}
	return &PendingNonceTracker{
		entries: make(map[string]nonceEntry),
		ttl:     ttl,
		now:     time.Now,
	}
}

// pendingNonceTracker is the process-wide instance, fed by SubmitToMempool.
var pendingNonceTracker = NewPendingNonceTracker(0)

// GetPendingNonceTracker exposes the singleton for consumers (RPC facade).
func GetPendingNonceTracker() *PendingNonceTracker { return pendingNonceTracker }

// Record notes a successfully routed transaction. Keeps the highest nonce
// per sender; refreshes the timestamp only when advancing (so a stale lower
// resubmit cannot extend the lifetime of a higher entry).
func (t *PendingNonceTracker) Record(sender string, nonce uint64) {
	if sender == "" {
		return
	}
	key := strings.ToLower(sender)

	t.mu.Lock()
	defer t.mu.Unlock()

	if e, ok := t.entries[key]; !ok || nonce > e.highestNonce {
		t.entries[key] = nonceEntry{highestNonce: nonce, recordedAt: t.now()}
	}
	t.pruneLocked()
}

// NextFor returns this node's view of the sender's next usable nonce given
// the confirmed account nonce: max(confirmed, trackedHighest+1), with expired
// entries ignored (and lazily dropped).
func (t *PendingNonceTracker) NextFor(sender string, confirmed uint64) uint64 {
	key := strings.ToLower(sender)

	t.mu.RLock()
	e, ok := t.entries[key]
	t.mu.RUnlock()

	if !ok || t.now().Sub(e.recordedAt) > t.ttl {
		if ok {
			t.mu.Lock()
			// re-check under write lock; another writer may have refreshed it
			if e2, still := t.entries[key]; still && t.now().Sub(e2.recordedAt) > t.ttl {
				delete(t.entries, key)
			}
			t.mu.Unlock()
		}
		return confirmed
	}

	if next := e.highestNonce + 1; next > confirmed {
		return next
	}
	return confirmed
}

// pruneLocked drops expired entries. Called under write lock; O(n) but n is
// bounded by active senders within the TTL window.
func (t *PendingNonceTracker) pruneLocked() {
	cutoff := t.now().Add(-t.ttl)
	for k, e := range t.entries {
		if e.recordedAt.Before(cutoff) {
			delete(t.entries, k)
		}
	}
}
