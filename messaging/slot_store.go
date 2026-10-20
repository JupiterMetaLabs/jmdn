package messaging

// M0.1 - monotonic slot counter (Architecture §7.1/§7.1b, build order M0 in
// §9.1). A height's period - its count of failed rounds - is stamped on the
// block by the proposer (proposer_period.go) and read back from the block
// everywhere else. M3 - EpochForSlot, the pure function
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
// number and its OWN final Period value (config.ZKBlock.Period - the number
// of failed rounds this height burned before committing, stamped by the
// proposer and bound into the block's ConsensusHash). The advance amount is
// period+1: one unit for every failed round, plus one for the commit itself -
// matching the §7.1 state diagram, read entirely from the committed block.
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
// DefaultProposerPeriods: the proposer's in-flight period for the next height
// is not recovered from history - after a restart it starts at 0 and resumes
// above already-signed rounds from the buddies' refusals (proposer_period.go).
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
// same package-level-default pattern as DefaultProposerPeriods
// (proposer_period.go) and SetEquivocationStore (consensus_hardening.go).
var DefaultSlotStore = NewSlotStore()

// LiveSlotFor returns the slot number the CURRENTLY-ACTIVE attempt at height
// occupies - i.e. the "S" in the §7.1 state diagram's "InSlot: begin slot S"
// - not merely the last value SlotStore folded in. Two components:
//
//   - DefaultSlotStore.Current(): every PAST height's contribution, folded in
//     only once each height actually committed.
//   - DefaultProposerPeriods.PeriodFor(height): the failed rounds this
//     height has burned so far on the proposer, live, even though the height
//     has not committed - each one burned a slot, but SlotStore only folds a
//     height's total in at its eventual commit (AdvanceOnCommit).
//
// Plus one: entering a fresh round always sets slot to (last resolved
// slot)+1 - the diagram never shows a same-slot re-entry.
//
// Proposer-only: it stamps the block being built RIGHT NOW. Verifiers check
// the result against the parent instead (Slot == parent.Slot + Period + 1,
// see linkageDecision), so they never need the proposer's counter.
func LiveSlotFor(height uint64) uint64 {
	return DefaultSlotStore.Current() + DefaultProposerPeriods.PeriodFor(height) + 1
}

// CurrentEpoch returns the selection epoch that is live RIGHT NOW on the
// proposer, including the failed rounds the next height has burned so far -
// composed from DefaultSlotStore (committed history) and
// DefaultProposerPeriods (the proposer's in-flight retry counter). A single
// height can burn enough failed rounds to cross an epoch boundary before it
// ever commits (§3.1/§7.1). nextHeight is the height not yet committed (i.e.
// current chain tip + 1).
//
// Deliberately NOT built on LiveSlotFor's "+1": CurrentEpoch answers "what
// epoch is live if nextHeight committed AT ITS CURRENT PERIOD right now",
// which is the already-resolved contribution only - the "+1" in LiveSlotFor
// is for stamping the block IN FLIGHT, a slot that hasn't resolved yet and
// so must not be counted as already-elapsed epoch progress.
func CurrentEpoch(nextHeight uint64) uint64 {
	return EpochForSlot(DefaultSlotStore.Current() + DefaultProposerPeriods.PeriodFor(nextHeight))
}
