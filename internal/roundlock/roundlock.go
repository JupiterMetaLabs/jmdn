// Package roundlock enforces, per node, that a validator never signs BOTH a
// block result and a timeout vote for the same consensus round (height,
// period) - the mutual-exclusion rule the timeout-certificate design calls
// §7.1b.
//
// # WHY THIS IS THE SAFETY-CRITICAL PIECE
//
// A block certificate needs 2f+1 of the k SEATED members (5 of 7); a timeout
// certificate needs 2/3 of the whole eligible POOL (20 of 29). Those quorums
// are over different sets, so the usual "two quorums must intersect in an
// honest node" argument does not by itself stop both certificates existing
// for the same round - which would let round p finalize a block at height h
// while the period advances and round p+1 finalizes a DIFFERENT block at h.
// What stops it is that every honest node refuses to put its signature on
// both sides of the same round. This package is that refusal, in one place,
// shared by both signing sites:
//
//   - the buddy's vote-result signer (AVC/BuddyNodes/MessagePassing), which
//     produces the signatures a block certificate counts, and
//   - the timeout-vote signer (messaging/timeout_gossip.go,
//     messaging/timeout_request.go).
//
// Whichever side a node signs first for a round wins; the other is refused
// for that round from then on. Asking again for the side already taken is
// allowed (idempotent), so a retried vote-result request is not refused.
//
// A round is identified by the period of the block being voted on. A
// TimeoutVote with Period p+1 is the claim "round p timed out", so the
// timeout signer locks round p.
//
// A Block reservation is also bound to the block hash being signed, so a node
// never signs two different blocks for the same round.
//
// Scope: in memory, plus a durable Store when SetStore is called (main wires
// the node's sync-state KV), so a restart cannot be used to sign a round a
// second time. Memory is bounded by pruning rounds far below the highest
// height seen; pruned rounds are re-read from the store.
package roundlock

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Side is what a node signed for a round.
type Side uint8

const (
	// Block: this node signed a block result (a vote-result BLS signature)
	// for a block at this round.
	Block Side = iota + 1
	// Timeout: this node signed a timeout vote claiming this round timed out.
	Timeout
)

func (s Side) String() string {
	switch s {
	case Block:
		return "block"
	case Timeout:
		return "timeout"
	default:
		return "none"
	}
}

// Round identifies one consensus round.
type Round struct {
	Height uint64
	Period uint64
}

// retainHeights bounds memory: rounds more than this many heights below the
// highest height ever locked are dropped. Far larger than any in-flight
// window; a round that old has long since committed or been superseded.
const retainHeights = 1024

// entry is what this node signed for one round: the side and, for a block
// signature, which block.
type entry struct {
	side Side
	hash string
}

// Store persists ledger entries so a restart cannot be used to sign a round
// twice. Get returns (nil, nil) for an absent key.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
}

// Ledger records, per round, which side this node has signed and, for a
// block, which block hash.
type Ledger struct {
	mu        sync.Mutex
	signed    map[Round]entry
	maxHeight uint64

	store  Store  // nil: in-memory only
	prefix string // store key namespace
}

// NewLedger returns an empty in-memory ledger.
func NewLedger() *Ledger { return &Ledger{signed: make(map[Round]entry)} }

// Default is the process-wide ledger both signing sites use.
var Default = NewLedger()

// HashBindingEnabled turns on block-hash binding for Block reservations and
// the Votes ledger. It must only be on when rounds can advance (timeout
// certificates move Period): the sequencer re-proposes a rejected height with
// a new block, and without a new Period every node that signed the rejected
// candidate would refuse the replacement and the chain would stop at that
// height. main sets it from the timeout-certificate wiring flag.
var HashBindingEnabled = false

// BoundHash returns hash when hash binding is enabled, and "" otherwise.
func BoundHash(hash string) string {
	if HashBindingEnabled {
		return hash
	}
	return ""
}

// Votes is a separate ledger for this node's individual ACCEPT votes (one
// block hash per round). It is independent of Default on purpose: a node
// that voted for a block that then failed to certify must still be able to
// sign a timeout vote for that round, or the timeout quorum could stall.
var Votes = NewLedger()

// SetStore makes the ledger durable: every reservation is written to store
// (under prefix) before it is granted, and rounds not in memory are looked up
// there. Call once at startup, before any signing.
func (l *Ledger) SetStore(store Store, prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store, l.prefix = store, prefix
}

func (l *Ledger) key(r Round) string {
	return fmt.Sprintf("%s%d:%d", l.prefix, r.Height, r.Period)
}

func encodeEntry(e entry) []byte { return []byte(fmt.Sprintf("%d:%s", e.side, e.hash)) }

func decodeEntry(raw []byte) (entry, bool) {
	s := string(raw)
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return entry{}, false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil || (Side(n) != Block && Side(n) != Timeout) {
		return entry{}, false
	}
	return entry{side: Side(n), hash: s[i+1:]}, true
}

// lookupLocked returns the entry for r from memory, falling back to the
// store. Caller holds l.mu.
func (l *Ledger) lookupLocked(r Round) (entry, bool, error) {
	if e, ok := l.signed[r]; ok {
		return e, true, nil
	}
	if l.store == nil {
		return entry{}, false, nil
	}
	raw, err := l.store.Get(l.key(r))
	if err != nil {
		return entry{}, false, err
	}
	if len(raw) == 0 {
		return entry{}, false, nil
	}
	e, ok := decodeEntry(raw)
	if !ok {
		// An unreadable record is treated as "something was signed": refuse
		// rather than risk signing a round twice.
		return entry{side: Block, hash: "\x00unreadable"}, true, nil
	}
	l.signed[r] = e
	return e, true, nil
}

// ErrStore is returned when a reservation could not be read or persisted.
// The caller must not sign.
var ErrStore = errors.New("roundlock: store unavailable")

// TryLock records that this node is about to sign side for round r. It
// returns ok=true when the node may sign (nothing signed yet for r, or the
// same side already), and ok=false with the side already taken when the
// OTHER side was signed first - the caller must then refuse to sign.
func (l *Ledger) TryLock(r Round, side Side) (ok bool, taken Side) {
	o, t, _ := l.TryLockFresh(r, side)
	return o, t
}

// TryLockFresh is TryLock, additionally reporting whether THIS call created the
// reservation.
//
// Only a caller that got fresh=true may Release on its own failure path. A
// re-entry (fresh=false) is riding a reservation an EARLIER attempt took, and
// that earlier attempt may already have produced and shipped a signature -
// TryLock is deliberately idempotent for the same side, so a retried
// vote-result request is not refused. Releasing on a re-entry would therefore
// clear a reservation that a real signature is standing behind, re-opening the
// round to the OTHER side and defeating the mutual exclusion this package
// exists to enforce.
//
// The failure mode this guards, in order:
//
//  1. vote-result for round r -> TryLock fresh -> ledger r->Block
//  2. BLS sign SUCCEEDS       -> the block signature ships and counts
//  3. RETRIED vote-result     -> TryLock re-entry, ok=true
//  4. that BLS sign FAILS     -> failure path releases
//  5. Release(r, Block)       -> side matches -> DELETE
//  6. round r unlocked        -> this node may now also sign a TIMEOUT for r
//
// Step 6 is both sides of one round signed by one node. Release's own
// compare-and-delete cannot catch it because the side matches; only knowing
// that step 3 was a re-entry can.
func (l *Ledger) TryLockFresh(r Round, side Side) (ok bool, taken Side, fresh bool) {
	ok, taken, _, fresh, _ = l.TryLockHash(r, side, "")
	return ok, taken, fresh
}

// TryLockHash is TryLockFresh that also binds a block reservation to one
// block hash: a second Block reservation for the same round with a different
// non-empty hash is refused, so this node never signs two different blocks
// for one round. hash is ignored for the Timeout side. On a store error the
// reservation is refused (ok=false, err=ErrStore-wrapped).
func (l *Ledger) TryLockHash(r Round, side Side, hash string) (ok bool, taken Side, takenHash string, fresh bool, err error) {
	if side != Block {
		hash = ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	prev, exists, lerr := l.lookupLocked(r)
	if lerr != nil {
		return false, 0, "", false, fmt.Errorf("%w: %v", ErrStore, lerr)
	}
	if exists {
		if prev.side != side {
			return false, prev.side, prev.hash, false, nil
		}
		if side == Block && hash != "" && prev.hash != "" && prev.hash != hash {
			return false, prev.side, prev.hash, false, nil
		}
		if side == Block && prev.hash == "" && hash != "" {
			// An unbound reservation (taken before hashes were recorded) is
			// bound to the first hash that asks.
			prev.hash = hash
			if perr := l.persistLocked(r, prev); perr != nil {
				return false, 0, "", false, perr
			}
			l.signed[r] = prev
		}
		return true, prev.side, prev.hash, false, nil
	}
	e := entry{side: side, hash: hash}
	if perr := l.persistLocked(r, e); perr != nil {
		return false, 0, "", false, perr
	}
	l.signed[r] = e
	if r.Height > l.maxHeight {
		l.maxHeight = r.Height
		l.pruneLocked()
	}
	return true, side, hash, true, nil
}

func (l *Ledger) persistLocked(r Round, e entry) error {
	if l.store == nil {
		return nil
	}
	if err := l.store.Put(l.key(r), encodeEntry(e)); err != nil {
		return fmt.Errorf("%w: %v", ErrStore, err)
	}
	return nil
}

// Signed reports which side, if any, this node has signed for r.
func (l *Ledger) Signed(r Round) (Side, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok, err := l.lookupLocked(r)
	if err != nil {
		return 0, false
	}
	return e.side, ok
}

// Release undoes a lock this node itself acquired via TryLock(r, side) but
// did not follow through on: TryLock said it was free to sign side for r, and
// the signing attempt that followed - the actual BLS/identity-key sign call -
// then failed (a key-load error, a signing error, or any other reason no
// valid signature was actually produced) rather than a decision made by this
// package.
//
// F-3 ("lock-then-fail stranding"): without this, TryLock's reservation is
// permanent for the process's life. A round whose intended side failed to
// sign is stranded - this node can never sign the OTHER side for it either,
// because TryLock refuses based on the side that was merely ATTEMPTED, never
// actually signed. That means the node contributes to NEITHER the block
// certificate NOR the timeout certificate for that round, for as long as the
// process runs: a liveness defect, and specifically the kind that gets harder
// to reach quorum on the more nodes it happens to, with no error anywhere
// that names the cause (the round just looks perpetually one vote short).
//
// SAFETY PRECONDITION: the caller MUST have obtained fresh=true from
// TryLockFresh for this exact (r, side). Release is safe ONLY then. Calling it
// after an idempotent re-entry (fresh=false) clears a reservation an earlier
// attempt took, which may already have shipped a real signature - that re-opens
// the round to the other side and defeats this package. The compare-and-delete
// below does NOT catch that case, because the side matches. Every caller
// (round_lock.go, messaging/timeout_gossip.go, messaging/timeout_request.go)
// therefore guards its release on fresh.
//
// Only clears the entry when it still holds EXACTLY (r, side) - i.e. nothing
// has changed what is recorded for r since this caller's own TryLock call.
// In practice the map only ever transitions from absent to one side and never
// from one side to the other (TryLock's ok=false branch never mutates it), so
// this is a belt-and-suspenders check against a caller bug (releasing the
// wrong side, or releasing twice) rather than protection against a real race.
//
// Callers MUST NOT call Release once a signature has actually been produced
// or broadcast - only when the attempt that followed TryLock genuinely failed
// to produce one. Calling it after a successful sign would re-open the round
// to the other side and defeat the whole package.
func (l *Ledger) Release(r Round, side Side) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, exists := l.signed[r]; exists && prev.side == side {
		delete(l.signed, r)
		if l.store != nil {
			// Best effort: if this fails the round stays reserved on disk,
			// which only costs liveness for this round, never safety.
			_ = l.store.Put(l.key(r), []byte{})
		}
	}
}

func (l *Ledger) pruneLocked() {
	if l.maxHeight <= retainHeights {
		return
	}
	floor := l.maxHeight - retainHeights
	for r := range l.signed {
		if r.Height < floor {
			delete(l.signed, r)
		}
	}
}
