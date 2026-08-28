package messaging

// Slot-restart fail-closed recovery (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md
// item 8's "remaining integration step") — 2026-08-24.
//
// slot_store.go's SeedFromCommittedTip existed and was tested, and
// DB_OPs/backend/block.go's toBlockRecord persisted Slot/Period into every
// committed block's ExtraData, but nothing called SeedFromCommittedTip at
// real node startup. Left as-is, a restarted node's DefaultSlotStore woke up
// at slot 0 with haveCommitted=false, and its FIRST post-restart commit hook
// call (broadcast.go / blockPropagation.go's AdvanceOnCommit) would silently
// treat that commit as the first one ever, self-correcting to a slot far
// below the network's true value — wrong committee/entropy epoch, no error,
// no log line an operator would notice before divergence.
//
// This file is the missing half: a startup (and post-catch-up) recovery path
// that reads this node's own locally-committed tip and seeds the counter
// BEFORE the node is allowed to vote, propose, or otherwise participate in
// consensus — see SlotStoreReady/MarkSlotStoreReady below, and the two gates
// that consult it: AVC/BuddyNodes/MessagePassing/consensus_sync_gate.go's
// consensusVoteReady (voting) and Block/consensus_fields.go's
// attachAVCConsensusFields (proposing).

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

// RecoverSlotStoreAtStartup seeds DefaultSlotStore from this node's local
// committed tip. Call exactly once, at startup, BEFORE the libp2p host is
// created and its stream handlers registered — i.e. before any commit hook
// (broadcast.go/blockPropagation.go's AdvanceOnCommit) can possibly fire.
// That ordering, not any locking here, is what makes this race-free: see
// main.go's call site, placed before node.NewNode.
//
// getTip must return:
//   - (tip, nil): a real committed block — its OWN Slot/BlockNumber are
//     adopted directly (see slot_store.go's SeedFromCommittedTip doc: tipSlot
//     already equals what AdvanceOnCommit would have produced live).
//   - (nil, err) wrapping ErrNoCommittedBlock: chain genuinely has no block
//     yet. Slot 0 is already correct — there is nothing to lose — so this is
//     treated as ready, but WITHOUT calling SeedFromCommittedTip and without
//     otherwise touching DefaultSlotStore, so a later real tip (this node's
//     own first commit, or bulk fast-sync — see EnsureSlotStoreRecovered)
//     can still seed it correctly. The pre-existing block-height sync gate
//     (consensus_sync_gate.go: "a node with no local chain NEVER votes")
//     independently blocks participation at height 0 regardless, so treating
//     this as ready here does not open any actual gap.
//   - (_, other error): a REAL read failure (DB unreachable, corrupt record,
//     etc). Fails closed — returns the error, does not touch the readiness
//     gate, so SlotStoreReady() stays false and the caller must not proceed
//     to start consensus.
//
// Also fails closed if the tip names a real height (BlockNumber > 0) but
// carries no persisted Slot/Period at all: that can only mean either the
// block predates the ExtraData persistence fix, or the read-back conversion
// broke — either way, adopting a silent zero here is exactly the bug this
// function exists to close, so it refuses instead.
func RecoverSlotStoreAtStartup(getTip func() (*config.ZKBlock, error)) error {
	tip, err := getTip()
	if err != nil {
		if errors.Is(err, ErrNoCommittedBlock) {
			log.Info().Msg("slot recovery: local chain has no committed block yet (genesis or unsynced) — SlotStore starts at slot 0, will be re-seeded once a real tip exists")
			MarkSlotStoreReady()
			return nil
		}
		return fmt.Errorf("slot recovery: reading local committed tip: %w", err)
	}
	if tip == nil {
		return fmt.Errorf("slot recovery: getTip returned a nil block with no error — refusing to treat that as success")
	}

	if tip.BlockNumber > 0 && tip.Slot == 0 && tip.Period == 0 {
		return fmt.Errorf("slot recovery: committed tip at height %d carries no persisted slot/period — cannot safely recover this node's epoch clock; refusing to start consensus until this is investigated", tip.BlockNumber)
	}

	if !DefaultSlotStore.SeedFromCommittedTip(tip.Slot, tip.BlockNumber) {
		return fmt.Errorf("slot recovery: SeedFromCommittedTip refused — SlotStore is already live; this function must run before any commit hook can fire (startup-ordering bug, not a data problem)")
	}
	log.Info().Uint64("tip_height", tip.BlockNumber).Uint64("tip_slot", tip.Slot).Uint64("tip_period", tip.Period).
		Msg("slot recovery: SlotStore seeded from local committed tip")
	MarkSlotStoreReady()
	return nil
}

// EnsureSlotStoreRecovered is the idempotent, re-callable form: safe to call
// again after RecoverSlotStoreAtStartup, in particular after a bulk fast-sync
// catch-up completes (§7.1's own documented gap: FastsyncV2 bulk writes do
// NOT go through the live commit hooks, so DefaultSlotStore.haveCommitted
// stays false throughout a catch-up — there is no live-hook race to guard
// against here, only a stale "no committed block yet" read from before the
// catch-up ran).
//
// No-ops (returns nil without calling getTip) once DefaultSlotStore is
// already live (SeedFromCommittedTip would refuse anyway) — this makes it
// safe to call from every fast-sync/reconcile success path without extra
// bookkeeping at the call site.
//
// Known, disclosed residual: this does not itself prevent a live gossip block
// from committing (and calling AdvanceOnCommit) in the narrow window between
// a fast-sync batch finishing and this function's own SeedFromCommittedTip
// call. Closing that fully needs an explicit lock shared with the commit
// hooks, which is a larger change than this task's scope; today the risk is
// bounded by the same block-height sync gate that already blocks voting more
// than MaxConsensusLagBlocks behind head, so any live block landing in that
// window necessarily comes from a peer this node is already near-caught-up
// with.
func EnsureSlotStoreRecovered(getTip func() (*config.ZKBlock, error)) error {
	if DefaultSlotStore.haveCommittedForRecoveryCheck() {
		return nil
	}
	return RecoverSlotStoreAtStartup(getTip)
}
