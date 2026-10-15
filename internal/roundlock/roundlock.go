// Package roundlock is the per-node signing ledger a buddy consults before it
// BLS-signs a block result. It enforces two rules for one consensus round
// (height, period):
//
//   - R1, sign once: this node signs at most ONE block per round. A block is
//     identified by its BlockHash together with its ConsensusHash - BlockHash
//     alone covers only the transactions, so a retry of the same transactions
//     at a new period shares it. Asking again for the block already signed is
//     allowed (idempotent), so a retried vote-result request is not refused.
//   - R2, never backwards: once this node has signed a block at (height, p) it
//     refuses any block at the same height with a period below p. The
//     sequencer only ever moves a height's period up after a failed round, so
//     a lower period can only be a delayed or replayed proposal.
//
// Every refusal reports the highest period this node has signed for the
// height. A sequencer that restarted mid-height (its period counter is
// in-memory) uses those reports to resume above every round it already burned
// - see messaging.ProposerPeriods.
//
// A height's records matter only until it commits. ForgetThrough, called from
// every local commit point (messaging.NoteHeightCommitted), records the
// committed tip, refuses any later request at or below it, and drops records
// more than commitMargin heights below it. Pruning is driven only by commits,
// never by the heights of incoming requests, so a request at a bogus huge
// height cannot clear the live height's records.
//
// Scope: in-memory. A restart forgets the ledger. The node then signs a
// height's current round again, which is safe for an honest sequencer: it
// never re-proposes a round it already left.
package roundlock

import "sync"

// Round identifies one consensus round.
type Round struct {
	Height uint64
	Period uint64
}

// Refusal says why TryLockFresh refused.
type Refusal uint8

const (
	// None: not refused.
	None Refusal = iota
	// Conflict: this node already signed a DIFFERENT block for this round (R1).
	Conflict
	// Stale: this node already signed a block at a higher period for this
	// height (R2).
	Stale
	// Committed: the height is at or below this node's committed tip; no
	// block at it can be accepted any more.
	Committed
)

func (r Refusal) String() string {
	switch r {
	case Conflict:
		return "conflict"
	case Stale:
		return "stale_period"
	case Committed:
		return "height_committed"
	default:
		return "none"
	}
}

// commitMargin is how many heights below the committed tip keep their
// records. Not needed for safety - TryLockFresh refuses every height at or
// below the tip - it is belt-and-braces against a commit point that reports
// out of order.
const commitMargin = 16

// Ledger records, per round, which block this node signed, and per height the
// highest period it signed.
type Ledger struct {
	mu            sync.Mutex
	signed        map[Round]string
	highest       map[uint64]uint64
	committed     uint64
	haveCommitted bool
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{signed: make(map[Round]string), highest: make(map[uint64]uint64)}
}

// Default is the process-wide ledger the buddy vote-result signer uses.
var Default = NewLedger()

// TryLockFresh records that this node is about to sign block for round r.
//
// It returns ok=true when the node may sign: nothing signed yet for r (fresh
// reservation), or the same block already (idempotent re-entry). It returns
// ok=false with the reason when R1 or R2 forbids signing. highest is the
// highest period this node has signed for r.Height after this call, reported
// either way so a refusal can carry it back to the sequencer.
//
// fresh is true only when THIS call created the reservation. Only such a
// caller may Release on its own signing failure (F-9): a re-entry rides a
// reservation an earlier attempt took, and that attempt may already have
// shipped a signature.
func (l *Ledger) TryLockFresh(r Round, block string) (ok bool, refusal Refusal, highest uint64, fresh bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	high, signedHere := l.highest[r.Height]
	if l.haveCommitted && r.Height <= l.committed {
		return false, Committed, high, false
	}
	if signedHere && r.Period < high {
		return false, Stale, high, false
	}
	if prev, exists := l.signed[r]; exists {
		if prev != block {
			return false, Conflict, high, false
		}
		return true, None, high, false
	}
	l.signed[r] = block
	if !signedHere || r.Period > high {
		l.highest[r.Height] = r.Period
		high = r.Period
	}
	return true, None, high, true
}

// Signed reports which block, if any, this node has signed for r.
func (l *Ledger) Signed(r Round) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.signed[r]
	return b, ok
}

// Highest reports the highest period this node has signed for height.
func (l *Ledger) Highest(height uint64) (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.highest[height]
	return p, ok
}

// Release undoes a reservation this node itself took via TryLockFresh(r, block)
// but did not follow through on: the BLS sign that followed failed, so no
// signature exists.
//
// F-3 ("lock-then-fail stranding"): without this, a round whose sign attempt
// failed would stay reserved for the process's life, and this node could never
// sign a block for it, nor (through R2) for any lower period of the height.
//
// SAFETY PRECONDITION: the caller MUST have obtained fresh=true from
// TryLockFresh for this exact (r, block). Releasing after an idempotent
// re-entry clears a reservation an earlier attempt may have signed behind,
// which would let this node sign a second block for the round (F-9).
//
// Only clears the entry when it still holds exactly block, and recomputes the
// height's highest period from what remains signed.
func (l *Ledger) Release(r Round, block string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, exists := l.signed[r]; !exists || prev != block {
		return
	}
	delete(l.signed, r)
	var high uint64
	found := false
	for other := range l.signed {
		if other.Height == r.Height && (!found || other.Period > high) {
			high, found = other.Period, true
		}
	}
	if found {
		l.highest[r.Height] = high
	} else {
		delete(l.highest, r.Height)
	}
}

// ForgetThrough records that height is committed locally. From then on
// TryLockFresh refuses every height at or below it, and records for heights
// commitMargin or more below it are dropped. The tip only moves up: a call
// with a height at or below the current tip is a no-op.
func (l *Ledger) ForgetThrough(height uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.haveCommitted && height <= l.committed {
		return
	}
	l.committed, l.haveCommitted = height, true
	if height < commitMargin {
		return
	}
	floor := height - commitMargin
	for r := range l.signed {
		if r.Height <= floor {
			delete(l.signed, r)
		}
	}
	for h := range l.highest {
		if h <= floor {
			delete(l.highest, h)
		}
	}
}

// CommittedTip reports the committed tip ForgetThrough last recorded.
func (l *Ledger) CommittedTip() (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.committed, l.haveCommitted
}
