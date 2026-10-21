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
// Durability: with a Store (SetStore, wired by main to the node's sync-state
// KV) every height's records are written before a reservation is granted and
// read back the first time a height is touched after a restart, so a restart
// cannot be used to sign a different block at a round already signed. A store
// read or write failure refuses the reservation (Unavailable). Without a
// Store the ledger is in-memory only.
package roundlock

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

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
	// Unavailable: the durable store could not be read or written, so the
	// ledger cannot prove the round is free. Fail closed.
	Unavailable
)

func (r Refusal) String() string {
	switch r {
	case Conflict:
		return "conflict"
	case Stale:
		return "stale_period"
	case Committed:
		return "height_committed"
	case Unavailable:
		return "ledger_unavailable"
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

	// Durable store (nil: in-memory only). loaded marks heights already read
	// back from the store this process; onDisk marks heights with a stored
	// record, so pruning clears exactly those.
	store  Store
	prefix string
	loaded map[uint64]bool
	onDisk map[uint64]bool
}

// Store persists ledger records. Get returns (nil, nil) or an empty value for
// an absent key.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
}

// NewLedger returns an empty in-memory ledger.
func NewLedger() *Ledger {
	return &Ledger{
		signed:  make(map[Round]string),
		highest: make(map[uint64]uint64),
		loaded:  make(map[uint64]bool),
		onDisk:  make(map[uint64]bool),
	}
}

// Default is the process-wide ledger the buddy vote-result signer uses.
var Default = NewLedger()

// Votes is the ledger for this node's own ACCEPT votes: one block per round,
// never a lower period of a height than one already voted for. Kept separate
// from Default because voting for a block and signing its aggregated result
// are different signatures, and refusing one must not block the other.
var Votes = NewLedger()

// BlockIdentity is the block key the ledgers compare: BlockHash covers only
// the transactions, so the ConsensusHash (Period, Slot and the other
// consensus fields) is what tells two proposals of the same transactions
// apart.
func BlockIdentity(blockHash, consensusHash string) string {
	return blockHash + "/" + consensusHash
}

// SetStore makes the ledger durable under the given key prefix. Call once at
// startup, before any signing.
func (l *Ledger) SetStore(store Store, prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store, l.prefix = store, prefix
}

// heightRecord is the stored form of one height: periods signed and the
// block signed at each.
type heightRecord struct {
	Signed map[string]string `json:"signed"`
}

func (l *Ledger) key(height uint64) string { return l.prefix + strconv.FormatUint(height, 10) }

// loadHeightLocked reads height's stored record into memory the first time
// the height is touched. Caller holds l.mu.
func (l *Ledger) loadHeightLocked(height uint64) error {
	if l.store == nil || l.loaded[height] {
		return nil
	}
	raw, err := l.store.Get(l.key(height))
	if err != nil {
		return fmt.Errorf("roundlock: read height %d: %w", height, err)
	}
	if len(raw) > 0 {
		var rec heightRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("roundlock: decode height %d: %w", height, err)
		}
		for ps, block := range rec.Signed {
			p, err := strconv.ParseUint(ps, 10, 64)
			if err != nil {
				return fmt.Errorf("roundlock: decode height %d: bad period %q", height, ps)
			}
			l.signed[Round{Height: height, Period: p}] = block
			if h, ok := l.highest[height]; !ok || p > h {
				l.highest[height] = p
			}
		}
		l.onDisk[height] = true
	}
	l.loaded[height] = true
	return nil
}

// persistHeightLocked writes height's current records. Caller holds l.mu.
func (l *Ledger) persistHeightLocked(height uint64) error {
	if l.store == nil {
		return nil
	}
	rec := heightRecord{Signed: map[string]string{}}
	for r, block := range l.signed {
		if r.Height == height {
			rec.Signed[strconv.FormatUint(r.Period, 10)] = block
		}
	}
	raw := []byte{}
	if len(rec.Signed) > 0 {
		var err error
		if raw, err = json.Marshal(rec); err != nil {
			return err
		}
	}
	if err := l.store.Put(l.key(height), raw); err != nil {
		return fmt.Errorf("roundlock: write height %d: %w", height, err)
	}
	l.onDisk[height] = len(raw) > 0
	return nil
}

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
	if l.haveCommitted && r.Height <= l.committed {
		return false, Committed, l.highest[r.Height], false
	}
	if err := l.loadHeightLocked(r.Height); err != nil {
		return false, Unavailable, l.highest[r.Height], false
	}
	high, signedHere := l.highest[r.Height]
	if signedHere && r.Period < high {
		return false, Stale, high, false
	}
	if prev, exists := l.signed[r]; exists {
		if prev != block {
			return false, Conflict, high, false
		}
		return true, None, high, false
	}
	// Persist before granting: if the record cannot be written, the
	// reservation is undone and refused, so a granted signature is always on
	// disk before it is produced.
	l.signed[r] = block
	if err := l.persistHeightLocked(r.Height); err != nil {
		delete(l.signed, r)
		return false, Unavailable, high, false
	}
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
	// Best effort: if this write fails the round stays reserved on disk,
	// which costs only liveness for this round after a restart, never safety.
	_ = l.persistHeightLocked(r.Height)
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
	for h := range l.loaded {
		if h <= floor {
			delete(l.loaded, h)
		}
	}
	// Clear stored records for pruned heights. Best effort: a record left
	// behind is never consulted again, since every height at or below the
	// committed tip is refused before the store is read.
	for h := range l.onDisk {
		if h <= floor {
			if l.store == nil || l.store.Put(l.key(h), []byte{}) == nil {
				delete(l.onDisk, h)
			}
		}
	}
}

// CommittedTip reports the committed tip ForgetThrough last recorded.
func (l *Ledger) CommittedTip() (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.committed, l.haveCommitted
}
