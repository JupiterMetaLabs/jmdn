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
	"gossipnode/Security"
	"gossipnode/config"
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
// VdfProof, SeedEpoch, and VotingSnapshotEpoch remain deliberately zero: the
// VDF proof rides only on the epoch-boundary block (§7.2, Stage E owns that)
// and the voting-snapshot checkpoint pointer (M9) is not built. Leaving those
// zero is honest — M2b's hash still covers them, so a relay cannot turn a zero
// into a nonzero. Do not synthesize placeholder values to make them look
// populated.
func attachAVCConsensusFields(block *config.ZKBlock) {
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

	if Security.M2bHashEnabled {
		block.BlockHash = Security.RecomputeBlockHashWithConsensusFields(block)
	}
}
