package messaging

// Slot clock (Architecture §7.1, M0.1) and its epoch mapping (M3).
//
// THE RULE: a block's slot is its block number. Slot == BlockNumber, always.
//
//   - height advances only when a block commits; a failed round retries at
//     the same height.
//   - period counts the failed rounds at one height (proposer_period.go). It
//     is stamped on the block and changes the committee seed, but it does NOT
//     move the slot: every attempt at height H, whatever its period, sits in
//     slot H.
//   - so a block's slot, and the entropy epoch it belongs to (EpochForSlot),
//     follow from its height alone. A node that restarts, falls behind or
//     catches up through sync derives exactly the same slot as everyone else
//     from the block number - there is no separate counter to recover,
//     migrate or let go stale.
//
// Verifiers enforce the rule on every received block: linkageDecision on the
// live path and CheckSyncedSlot on ThebeSync.
//
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

// SlotStore records the highest slot - which is the highest block number -
// this node has committed. It is a cache of the committed tip, not an
// independent clock: every commit point (live receive, the proposer's own
// commit, ThebeSync, startup recovery) raises it through
// NoteHeightCommitted, and raising to the maximum is order-independent, so a
// sync and a live commit racing each other cannot leave it behind.
type SlotStore struct {
	mu   sync.RWMutex
	slot uint64 // highest committed height (== its slot)

	// haveCommitted distinguishes "nothing committed yet" from a committed
	// genesis block at height 0.
	haveCommitted bool
}

// NewSlotStore returns a fresh store - nothing committed yet.
func NewSlotStore() *SlotStore {
	return &SlotStore{}
}

// Current returns the slot of the highest block committed so far.
func (s *SlotStore) Current() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.slot
}

// AdvanceOnCommit records that the block at height is committed: the store
// moves up to height's slot (== height). Returns (newSlot, true) when the
// store moved, (currentSlot, false) when height is not above what is already
// recorded - a duplicate or out-of-order notification is a no-op, never a
// regression.
func (s *SlotStore) AdvanceOnCommit(height uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.haveCommitted && height <= s.slot {
		return s.slot, false
	}
	s.slot = height
	s.haveCommitted = true
	return s.slot, true
}

// DefaultSlotStore is the process-wide store NoteHeightCommitted raises -
// same package-level-default pattern as DefaultProposerPeriods
// (proposer_period.go) and SetEquivocationStore (consensus_hardening.go).
var DefaultSlotStore = NewSlotStore()

// LiveSlotFor returns the slot a block at height carries: its height. Every
// attempt at a height - the first and every retry - shares that slot; the
// retry is told apart by its Period.
func LiveSlotFor(height uint64) uint64 {
	return height
}
