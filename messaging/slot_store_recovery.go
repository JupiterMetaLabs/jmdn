package messaging

// Slot recovery at startup and after catch-up.
//
// A block's slot is its block number (slot_store.go), so recovery has nothing
// to reconstruct: it reads this node's committed tip and records its height
// (NoteHeightCommitted), which also arms the per-round signing ledgers. Its
// remaining job is to fail closed when the tip cannot be READ - until then
// this node must not vote or propose. See SlotStoreReady/MarkSlotStoreReady
// below and the two gates that consult it:
// AVC/BuddyNodes/MessagePassing/consensus_sync_gate.go's consensusVoteReady
// (voting) and Block/consensus_fields.go's attachAVCConsensusFields
// (proposing).

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"gossipnode/config"

	"github.com/rs/zerolog/log"
)

// EnforceSlotRecoveryGate is the single knob both the vote path
// (AVC/BuddyNodes/MessagePassing/consensus_sync_gate.go) and the propose path
// (Block/consensus_fields.go's attachAVCConsensusFields) check before acting.
// Default-ON: this is a purely LOCAL per-node safety check (unlike the
// *_WIRING/*_AGG_CERT flags elsewhere in this codebase, it changes no wire
// format and needs no fleet-wide coordination), so there is no reason to
// ship it inert. Set JMDN_ENFORCE_SLOT_RECOVERY_GATE=0 only for a harness
// that deliberately never wires DefaultSlotStore/RecoverSlotStoreAtStartup at
// all (e.g. an isolated unit test of unrelated logic that happens to route
// through the vote or propose path).
var EnforceSlotRecoveryGate = os.Getenv("JMDN_ENFORCE_SLOT_RECOVERY_GATE") != "0"

// slotStoreReady gates consensus participation on this node. Fail-closed by
// construction: zero value is false, so any code path that forgets to call
// the recovery function below leaves the node correctly blocked rather than
// silently permitted — the opposite failure mode of what got us here.
var slotStoreReady atomic.Bool

// SlotStoreReady reports whether this node's slot/epoch clock has been
// recovered from — or is legitimately consistent with — its local committed
// history, and it is therefore safe for this node to vote or propose.
//
// Consulted, not derived: nothing here re-checks the DB on every call, so a
// caller on a hot path (every vote, every proposal) pays only an atomic load.
func SlotStoreReady() bool { return slotStoreReady.Load() }

// MarkSlotStoreReady flips the gate open. Exported for the startup path
// (main.go) and for tests that need to bypass recovery; production code that
// wants to know if it may proceed should call SlotStoreReady(), not this.
func MarkSlotStoreReady() { slotStoreReady.Store(true) }

// ResetSlotStoreReadyForTest clears the gate. Test-only.
func ResetSlotStoreReadyForTest() { slotStoreReady.Store(false) }

// ErrNoCommittedBlock is what a getTip function passed to
// RecoverSlotStoreAtStartup must return (wrapped or bare, checked with
// errors.Is) when this node's local chain has no committed block at all —
// a legitimate state (true network genesis, or a brand-new node that has
// not synced anything yet), distinct from a READ FAILURE against a chain
// that does have history. Conflating the two would either fail closed
// forever on a genuinely empty chain, or fail open on a broken DB read —
// both wrong in different directions, which is why this is a distinct
// sentinel rather than "any error means empty."
var ErrNoCommittedBlock = errors.New("messaging: no committed block found (empty local chain)")

// RecoverSlotStoreAtStartup records this node's local committed tip. Call it
// at startup before the node may vote or propose, and again after any bulk
// catch-up (EnsureSlotStoreRecovered).
//
// getTip must return:
//   - (tip, nil): a real committed block - its height is recorded, and since
//     the slot is the height, no stored slot field is needed. A tip written
//     before slots were persisted (Slot 0 at a real height) recovers like any
//     other.
//   - (nil, err) wrapping ErrNoCommittedBlock: the chain genuinely has no
//     block yet. Ready, with nothing recorded; the block-height sync gate
//     (consensus_sync_gate.go: "a node with no local chain NEVER votes")
//     still blocks participation at height 0.
//   - (_, other error): a real read failure (DB unreachable, corrupt record).
//     Fails closed - returns the error and leaves the readiness gate closed,
//     so the caller must not start consensus.
func RecoverSlotStoreAtStartup(getTip func() (*config.ZKBlock, error)) error {
	tip, err := getTip()
	if err != nil {
		if errors.Is(err, ErrNoCommittedBlock) {
			log.Info().Msg("slot recovery: local chain has no committed block yet (genesis or unsynced) — nothing to record until a real tip exists")
			MarkSlotStoreReady()
			return nil
		}
		return fmt.Errorf("slot recovery: reading local committed tip: %w", err)
	}
	if tip == nil {
		return fmt.Errorf("slot recovery: getTip returned a nil block with no error — refusing to treat that as success")
	}
	if tipSlotMisaligned(tip) {
		// Warn, not refuse: the node itself runs correctly from here, and the
		// tip alone cannot say how much history is affected. Peers syncing
		// from this node will reject those blocks (CheckSyncedSlot), so the
		// operator must run the scan.
		log.Warn().Uint64("tip_height", tip.BlockNumber).Uint64("stored_slot", tip.Slot).
			Msg("slot recovery: stored tip slot does not equal its block number — this chain predates the slot = block number rule; peers syncing from this node will reject those blocks. Run Scripts/check_slot_alignment.sh")
	}
	NoteHeightCommitted(tip.BlockNumber)
	log.Info().Uint64("tip_height", tip.BlockNumber).Uint64("slot", DefaultSlotStore.Current()).
		Msg("slot recovery: recorded local committed tip (slot = block number)")
	MarkSlotStoreReady()
	return nil
}

// tipSlotMisaligned reports whether a stored tip breaks the slot rule. Slot 0
// is a tip written before slots were persisted, which sync also accepts.
func tipSlotMisaligned(tip *config.ZKBlock) bool {
	return tip.Slot != 0 && tip.Slot != tip.BlockNumber
}

// EnsureSlotStoreRecovered re-reads the committed tip and records it. Call it
// after every bulk catch-up (FastsyncV2 / reconcile), whose block writes do
// not go through the live commit hooks: it moves the slot store, and the
// signing ledgers, up to the new tip. Safe to call any number of times and in
// any order with live commits - recording only ever raises.
func EnsureSlotStoreRecovered(getTip func() (*config.ZKBlock, error)) error {
	return RecoverSlotStoreAtStartup(getTip)
}
