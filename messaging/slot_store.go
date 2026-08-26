package messaging

// M0.1 - monotonic slot counter (Architecture §7.1/§7.1b, build order M0 in
// §9.1, distinct sub-item from M0.2 - period/timeout certificates, already
// landed in timeout_certificates.go). M3 - EpochForSlot, the pure function
// that turns a slot into the selection epoch RANDAO keys on (§3.1).
//
// `slot` is a DIFFERENT counter from `height` and `period`:
//   - height only advances on a committed block; a failed round retries at
//     the same height.
//   - period resets to 0 on commit, +1 on a certified timeout, scoped to one
//     height.
//   - slot never resets and never skips backward - it advances by
//     (period-at-commit + 1) every time a height finally commits, whatever
//     that height's own retry history was. This is what makes slot fold a
//     stalled/retried round INTO the epoch clock instead of freezing it -
//     see the §7.1 state diagram: TimedOut -> Cert -> slot+1, period+1,
//     height unchanged; Committed -> slot+1, height+1, period 0. Either path
//     advances slot by exactly one unit of "this round is now resolved."
//
// LIVE-ONLY, BY EXPLICIT SCOPE DECISION 2026-08-19: this tracks slot in
// memory, advanced from the two verified commit hooks
// (messaging/broadcast.go's ProcessBlockLocally, messaging/blockPropagation.go's
// receive-and-store path). It does NOT survive a process restart correctly,
// and does NOT advance during FastsyncV2 bulk catch-up (both paths bypass
// these hooks). Root cause, traced directly: DB_OPs/backend/block.go's
// toBlockRecord never copies Slot/Period/RandaoReveals/VdfProof/SeedEpoch/
// VotingSnapshotEpoch into the persisted thebegateway.BlockRecord - not even
// into its ExtraData JSONB catch-all (which IS used, but only for the
// unrelated ZK-proof fields). Fixing that is a separate, not-yet-scoped
// persistence task; this file's known limitation is documented here
// deliberately rather than silently worked around. Safe for a
// continuously-running node (what manual testing needs); do not rely on it
// across a restart or a fast-sync jump until persistence is fixed.

import "sync"

// N is the number of slots per epoch - Architecture §3.1/§9.1's M3 row,
// adopted parameter (VDF-Implementation-Handoff.md §0, sourced from
// Low-Level-Design §1). Fleet-wide constant, not per-node config: every node
// must derive the identical epoch from the identical slot, or they seat
// different committees from the same chain state (§7.1b).
const N = 50

// EpochForSlot returns the selection epoch a slot belongs to - RANDAO,
// the reveal cutoff, and the committee-seed formula all key on this value
// (§3.1's binding: "RANDAO runs on the SELECTION epoch, slot/N - not the
// wall-clock snapshot epoch"). Pure integer division, no rounding tricks:
// epoch E covers slots [E*N, (E+1)*N).
func EpochForSlot(slot uint64) uint64 {
	return slot / N
}

// EpochBoundarySlot returns the first slot of epoch — the inverse edge of
// EpochForSlot (EpochForSlot(EpochBoundarySlot(e)) == e, and it is the
// unique slot in epoch e for which that holds at the low end). The VDF
// proof is attached only on this slot's block (§7.2) — see
// Block/consensus_fields.go.
func EpochBoundarySlot(epoch uint64) uint64 {
	return epoch * N
}

// SlotStore tracks the live, in-memory slot counter for this node. Every
// node computes its OWN slot from the certified events it has observed -
// never a wall clock, never copied from a peer (§7.1b's global-agreement
// guarantee rests on this).
type SlotStore struct {
	mu   sync.RWMutex
	slot uint64 // starts at 0 at AVC activation, per Architecture §7.1

	// lastCommittedHeight guards against double-counting: a replayed or
	// duplicate commit notification for a height already folded into slot
	// must be a no-op, not a second advance. A node's own broadcast block
	// echoing back through its own gossip receive path is the concrete case
	// this exists for.
	lastCommittedHeight uint64
	haveCommitted       bool
}

// NewSlotStore returns a fresh store - slot 0, no height committed yet.
func NewSlotStore() *SlotStore {
	return &SlotStore{}
}

// haveCommittedForRecoveryCheck reports whether this store has ever advanced
// (live commit or a prior successful SeedFromCommittedTip). Used only by
// EnsureSlotStoreRecovered (slot_store_recovery.go) to decide whether a
// repeat recovery attempt is still meaningful — a live/seeded store can never
// be seeded again anyway (SeedFromCommittedTip refuses), so this lets the
// caller skip even the getTip read in that case.
func (s *SlotStore) haveCommittedForRecoveryCheck() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.haveCommitted
}

// Current returns the slot value as of the last accepted commit.
func (s *SlotStore) Current() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.slot
}

// AdvanceOnCommit folds one resolved height into the slot counter. Call this
// once, at the moment a height is durably committed, with that height's OWN
// number and its OWN final Period value (config.ZKBlock.Period - already
// correct thanks to M0.2's PeriodStore, which tracks exactly how many
// certified timeouts this height burned before committing). The advance
// amount is period+1: one unit for every timeout this height survived, plus
// one for the commit itself - matching the §7.1 state diagram exactly,
// without needing a second, separate live hook into the timeout-certificate
// path (which would risk double-counting during any future catch-up work).
//
// Returns (newSlot, true) if this height was newer than the last one folded
// in and the counter advanced; (currentSlot, false) if height was equal to
// or older than the last one seen - a no-op, not an error, so a duplicate or
// out-of-order delivery can never regress or double-advance the counter.
func (s *SlotStore) AdvanceOnCommit(height, period uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.haveCommitted && height <= s.lastCommittedHeight {
		return s.slot, false
	}

	s.slot += period + 1
	s.lastCommittedHeight = height
	s.haveCommitted = true
	return s.slot, true
}

// SeedFromCommittedTip recovers the counter after a restart or a fast-sync
// catch-up, from the last committed block's OWN Slot field (persisted per
// docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8 — the block already carries
// the fully-folded value, so this is a direct adopt, not a recomputation:
// tipSlot already equals what AdvanceOnCommit would have produced had this
// process been running the whole time).
//
// Only takes effect on a store that has never been advanced live - checked
// the same way AdvanceOnCommit checks for a stale/duplicate height, so a
// startup call that races a live commit hook can never win and clobber real
// progress. Returns false (no-op) once the store is already live, or once it
// has already been seeded once (a second seed call is never legitimate: the
// store is either fresh-and-unseeded, or it isn't).
//
// Call this ONCE at startup, before any commit hook can fire, with the tip
// block's own Slot and BlockNumber. Deliberately does not touch
// DefaultPeriodStore: the live in-flight period for whatever height hasn't
// committed yet is correctly re-derived from scratch as the node resyncs
// timeout certificates over gossip, not from anything that needs recovering
// here.
func (s *SlotStore) SeedFromCommittedTip(tipSlot, tipHeight uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.haveCommitted {
		return false
	}
	s.slot = tipSlot
	s.lastCommittedHeight = tipHeight
	s.haveCommitted = true
	return true
}

// DefaultSlotStore is the process-wide store the two commit hooks
// (messaging/broadcast.go, messaging/blockPropagation.go) both advance -
// same package-level-default pattern as DefaultPeriodStore
// (timeout_certificates.go) and SetEquivocationStore (consensus_hardening.go).
var DefaultSlotStore = NewSlotStore()

// LiveSlotFor returns the slot number the CURRENTLY-ACTIVE attempt at height
// occupies - i.e. the "S" in the §7.1 state diagram's "InSlot: begin slot S"
// - not merely the last value SlotStore folded in. Two components:
//
//   - DefaultSlotStore.Current(): every PAST height's contribution, folded in
//     only once each height actually committed.
//   - DefaultPeriodStore.PeriodFor(height): height's OWN already-accepted
//     timeout certificates, live, even though none of them have committed
//     yet - each one already advanced slot per the state diagram
//     (TimedOut -> Cert -> slot+1, period+1) but SlotStore only folds a
//     height's total in at its eventual commit, not per intermediate
//     timeout. This term is exactly the part SlotStore hasn't seen yet.
//
// Plus one: entering a fresh InSlot state (whether from a commit's NextNew
// or a timeout's NextSame) always sets slot to (last resolved slot)+1 - the
// diagram never shows a same-slot re-entry.
//
// This is NOT the same as CurrentEpoch's composition by accident - both read
// the identical two stores for the identical reason (SlotStore is
// commit-lagged; PeriodStore is not); CurrentEpoch stops at dividing by N,
// this stops one step earlier at the raw slot number a producer needs to
// stamp on the block it is building RIGHT NOW for `height`.
func LiveSlotFor(height uint64) uint64 {
	return DefaultSlotStore.Current() + DefaultPeriodStore.PeriodFor(height) + 1
}

// CurrentEpoch returns the selection epoch that is live RIGHT NOW, including
// any in-flight (not-yet-committed) timeout churn at the next height -
// composed from DefaultSlotStore (committed history) and DefaultPeriodStore
// (M0.2, the live period counter for whichever height hasn't committed yet).
// This matters because a single height can burn enough certified timeouts to
// cross an epoch boundary before it ever commits (§3.1/§7.1) - a caller that
// only read DefaultSlotStore.Current() would under-report the epoch until
// the next commit finally lands. nextHeight is the height not yet committed
// (i.e. current chain tip + 1).
//
// Deliberately NOT built on LiveSlotFor's "+1": CurrentEpoch answers "what
// epoch is live if nextHeight committed AT ITS CURRENT PERIOD right now",
// which is the already-resolved contribution only - the "+1" in LiveSlotFor
// is for stamping the block IN FLIGHT, a slot that hasn't resolved yet and
// so must not be counted as already-elapsed epoch progress.
func CurrentEpoch(nextHeight uint64) uint64 {
	pendingPeriod := DefaultPeriodStore.PeriodFor(nextHeight)
	return EpochForSlot(DefaultSlotStore.Current() + pendingPeriod)
}
