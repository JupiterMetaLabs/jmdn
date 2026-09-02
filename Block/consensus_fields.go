package Block

// M2b producer-side wiring (Architecture §8, Low-Level-Design §2) +
// VDF-Implementation-Handoff.md §6's corrected attachment point: the six AVC
// consensus fields get set HERE, inside jmdn, right before consensus.Start -
// not in JMDT-Sequencer-Orchestrator (verified earlier this session: block
// gossip uses config.ZKBlock's own JSON tags, bypassing the orchestrator's
// proto entirely, so attaching fields here is both sufficient and the only
// place that actually matters).
//
// Call attachAVCConsensusFields(&block) (or attachAVCConsensusFields(block)
// if already a pointer) exactly once, after all other block validation has
// passed and immediately before Sequencer.NewConsensus/.Start - see the two
// call sites in Server.go and grpc_server.go.

import (
	"fmt"

	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/messaging"
)

// attachAVCConsensusFields sets the two fields that already have a real,
// live source (Slot, Period - from this morning's M0.1/M3 work) and, only
// when the M2b rollout flag is on, recomputes BlockHash to cryptographically
// bind all six consensus fields plus transaction contents. This OVERWRITES
// whatever BlockHash the caller (today, effectively the orchestrator's own
// legacy formula) supplied - required, since the six-field hash cannot be
// computed by anything upstream of jmdn, which is the only place these
// fields exist. Every other node re-derives and checks this same hash via
// Security.CheckBlockHash / messaging.checkBodyBinding on receipt - already
// wired and tested (Security/blockhash_m2b_flag_test.go,
// messaging/body_binding_m2b_flag_test.go) - so this producer-side write is
// the missing half of an already-closed loop, not a new one.
//
// RandaoReveals is now populated — CHANGED 2026-08-20. It was previously left
// at zero with the note "the entropy-committee reveal pipeline (M4) is not
// built yet, so there is no real value to put in them." M4's reveal mechanism
// now exists (Architecture §4.3 Decision A: deterministic ed25519 signatures),
// and messaging.RevealsForBlock supplies the real, already-verified values.
//
// This assignment was THE missing link in the entropy pipeline. Every stage
// downstream of it — fold, finalise, VDF seal — was wired and tested, but no
// code anywhere assigned block.RandaoReveals, so every block shipped empty,
// every epoch saw 0 of m reveals, and Rule 1 sent every single epoch to
// fallback. Nothing downstream could have detected that as an anomaly, because
// "no reveals arrived" is exactly what a fully-censored epoch looks like.
//
// RevealsForBlock returns nil outside the reveal window [E*N, E*N+K), so this
// is a no-op on the large majority of blocks (47 of every 50 at N=50, K=3),
// and it is ordered by peer ID so two nodes assembling from the same inbox
// produce byte-identical lists — required once M2b hashes the reveal array in
// order.
//
// VdfProof and SeedEpoch are now populated — CHANGED (VDF pipeline
// completion pass). They are attached ONLY on the epoch-boundary block
// (block.Slot == messaging.EpochBoundarySlot(epoch), §7.2) via
// Sequencer.SealerResultFor; every other block leaves them at zero, same
// style as RandaoReveals outside its reveal window (a silent no-op by
// design, not an error). On the boundary block itself, a proof that is not
// yet ready fails closed (Sequencer.ErrVDFProofNotReady) rather than
// proposing with a missing/zero entropy value for its own epoch — see
// Sequencer.SealerResultFor's doc comment.
//
// VotingSnapshotEpoch remains deliberately zero: the voting-snapshot
// checkpoint pointer (M9) is not built. Leaving it zero is honest — M2b's
// hash still covers it, so a relay cannot turn a zero into a nonzero. Do not
// synthesize a placeholder value to make it look populated.
// attachAVCConsensusFields now returns an error, checked FIRST and fail-closed
// (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8): a node whose slot/epoch
// clock has not been recovered from its own committed history (see
// messaging.RecoverSlotStoreAtStartup / SlotStoreReady) must not PROPOSE
// either, not only vote — block.Slot below is read straight off
// DefaultSlotStore via LiveSlotFor, and stamping a wrong slot onto a
// proposed block is exactly as dangerous as voting on one, arguably worse
// since it is this node's own error propagating outward to every voter.
// Mirrors the vote-side gate in
// AVC/BuddyNodes/MessagePassing/consensus_sync_gate.go's consensusVoteReady;
// same knob (messaging.EnforceSlotRecoveryGate), so one env var controls both.
func attachAVCConsensusFields(block *config.ZKBlock) error {
	if messaging.EnforceSlotRecoveryGate && !messaging.SlotStoreReady() {
		return fmt.Errorf("attachAVCConsensusFields: SlotStore not recovered — refusing to propose block %d until this node's slot/epoch clock is confirmed consistent with its committed history", block.BlockNumber)
	}
	block.Slot = messaging.LiveSlotFor(block.BlockNumber)
	block.Period = messaging.DefaultPeriodStore.PeriodFor(block.BlockNumber)
	block.RandaoReveals = messaging.RevealsForBlock(block.Slot)
	// B1 (Architecture §4.2a, §10 decision 10) — attach the PREVIOUS block's
	// commit certificate when this block's parent sits in the epoch's fallback
	// fold window. Nil on ~90% of blocks and whenever JMDN_AVC_AGG_CERT is off.
	// It must be the parent's, not this block's: the buddies sign THIS block's
	// hash, so its own certificate cannot be an input to that hash.
	if block.BlockNumber > 0 {
		block.PrevAggCert = messaging.CertificateForBlockAssembly(block.Slot, block.BlockNumber-1)
	}

	// R4 (buddy staking rewards, docs/STAKING-REWARDS-DESIGN.md §4) — populate
	// FeeRecipients from the PREVIOUS block's certifiers (block.PrevAggCert, set
	// just above), each weighted by its bound reward address's balance at the
	// parent (N-1) committed state. Gated OFF by default: with
	// consensus.reward_split_enabled false this is a no-op and the block keeps
	// empty FeeRecipients (single-coinbase credit, byte-identical to today). The
	// recipients are a PURE FUNCTION of already-agreed inputs (parent certifiers,
	// the authenticated reward-address map, parent-state balances, the fleet-uniform
	// StakeWeight constants), and every node recomputes+validates them on receive
	// (R5), so the sequencer has no freedom over the split. FAIL CLOSED: any error
	// (unset reward source, balance read, invalid bound address) aborts the block
	// build rather than proposing a wrong/half split.
	// Guard settings.Get() with IsLoaded() (it panics before Load()) so this stays
	// robust to init order and so unit tests that exercise attachAVCConsensusFields
	// without loading config still see reward-split OFF (default) — byte-identical.
	if settings.IsLoaded() && settings.Get().Consensus.RewardSplitEnabled {
		// Reward the PREVIOUS block's certifiers, sourced from that block's
		// persisted committee certificate (present on every certified block) — NOT
		// block.PrevAggCert, which only exists in the entropy fold window when
		// JMDN_AVC_AGG_CERT is on. This makes the split fire on EVERY block. R5
		// (messaging.checkFeeRecipients) recomputes from the identical source.
		signers, serr := messaging.PrevBlockCertSigners(block.BlockNumber - 1)
		if serr != nil {
			return fmt.Errorf("attachAVCConsensusFields: reading prev-block certifiers for block %d: %w", block.BlockNumber, serr)
		}
		recipients, err := messaging.ExpectedFeeRecipients(signers)
		if err != nil {
			return fmt.Errorf("attachAVCConsensusFields: deriving fee recipients for block %d: %w", block.BlockNumber, err)
		}
		block.FeeRecipients = recipients
	}

	epoch := messaging.EpochForSlot(block.Slot)

	// Committee-snapshot anchor (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md items
	// 1/6/8) — empty unless JMDN_COMMITTEE_SNAPSHOT_ANCHOR is on AND this
	// block's slot's epoch has already been frozen (messaging's
	// maybeFreezeUpcomingSnapshot, called from the commit hooks). Repeated on
	// every block of the epoch once frozen, not just one boundary block, so
	// a rejoining node can recover it from whichever block it happens to sync
	// to first, not one specific block that might be missed.
	if h, ok := messaging.FrozenCommitteeSnapshotHashFor(epoch); ok {
		block.CommitteeSnapshotHash = h[:]
	}

	// VDF proof attachment (VDF-Implementation-Handoff.md §6) — only on the
	// epoch-boundary block. Off-boundary blocks leave VdfProof/SeedEpoch at
	// zero (see the header comment above); this is not an error.
	if block.Slot == messaging.EpochBoundarySlot(epoch) {
		result, ok := Sequencer.SealerResultFor(epoch)
		if !ok {
			return fmt.Errorf("attachAVCConsensusFields: %w: epoch %d, block %d",
				Sequencer.ErrVDFProofNotReady, epoch, block.BlockNumber)
		}
		if result.Err != nil {
			return fmt.Errorf("attachAVCConsensusFields: VDF sealing failed for epoch %d: %w", epoch, result.Err)
		}
		raw, err := result.Proof.MarshalBinary()
		if err != nil {
			return fmt.Errorf("attachAVCConsensusFields: encoding VDF proof for epoch %d: %w", epoch, err)
		}
		block.VdfProof = raw
		block.SeedEpoch = epoch
	}

	// BlockHash is the orchestrator-submitted, transactions-only identity and is
	// NEVER overwritten here. Consensus fields (Slot/Period/PrevAggCert/
	// FeeRecipients/CommitteeSnapshotHash/VdfProof) travel as their own advisory
	// fields on the block, like AccountNonces — they do not mutate BlockHash.
	// Rebinding BlockHash to the consensus-fields hash broke every tx-only
	// validator (Security.CheckZKBlockValidation, messaging.checkBodyBinding, the
	// AVC structural validator) and the vote, which all recompute the tx-only
	// hash. If the six consensus fields must be signature-covered (M2b's goal),
	// that binding belongs in a SEPARATE ConsensusHash field the vote domain signs
	// — not in BlockHash. RecomputeBlockHashWithConsensusFields is retained for
	// that use.
	//
	// ConsensusHash: the SEPARATE consensus-fields digest, set AFTER all six
	// fields + PrevAggCert + CommitteeSnapshotHash + FeeRecipients are populated
	// above. This is the value the committee's v4 vote signs over, giving those
	// fields tamper-evidence without mutating BlockHash. Deterministic: every node
	// recomputes it from the received block (messaging.checkConsensusBinding).
	block.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(block)
	return nil
}
