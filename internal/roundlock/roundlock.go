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
// Scope: in-memory. A process restart forgets the locks. That matches the
// PeriodStore and SlotStore it sits beside (also in-memory, documented in
// messaging/slot_store.go); durable persistence belongs with that task.
// Memory is bounded by pruning rounds far below the highest height seen.
package roundlock

import "sync"

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

// Ledger records, per round, which side this node has signed.
type Ledger struct {
	mu        sync.Mutex
	signed    map[Round]Side
	maxHeight uint64
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{signed: make(map[Round]Side)} }

// Default is the process-wide ledger both signing sites use.
var Default = NewLedger()

// TryLock records that this node is about to sign side for round r. It
// returns ok=true when the node may sign (nothing signed yet for r, or the
// same side already), and ok=false with the side already taken when the
// OTHER side was signed first - the caller must then refuse to sign.
func (l *Ledger) TryLock(r Round, side Side) (ok bool, taken Side) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, exists := l.signed[r]; exists {
		return prev == side, prev
	}
	l.signed[r] = side
	if r.Height > l.maxHeight {
		l.maxHeight = r.Height
		l.pruneLocked()
	}
	return true, side
}

// Signed reports which side, if any, this node has signed for r.
func (l *Ledger) Signed(r Round) (Side, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.signed[r]
	return s, ok
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
// This does not weaken the safety property TryLock exists to enforce. Release
// only reverses a reservation THIS caller itself just took and never used; it
// can never let the OTHER side in once a real signature actually shipped,
// because a caller that succeeded has no reason to call Release, and the
// package's own callers (round_lock.go, messaging/timeout_gossip.go,
// messaging/timeout_request.go) only call it on their own sign-attempt's
// error path - see each call site's comment.
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
	if prev, exists := l.signed[r]; exists && prev == side {
		delete(l.signed, r)
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
