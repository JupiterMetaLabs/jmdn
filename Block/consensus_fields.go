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
// RandaoReveals, VdfProof, SeedEpoch, and VotingSnapshotEpoch are
// deliberately left at their zero value here. Verified 2026-08-19
// (AVC-M4-Entropy-Reveal-Pipeline-Design.md): the entropy-committee reveal
// pipeline (M4) and the voting-snapshot checkpoint pointer (M9) are not
// built yet, so there is no real value to put in them. Leaving them zero is
// honest, not a shortcut - M2b's hash still covers them (a relay cannot
// silently turn a zero into a nonzero either, or vice versa), it simply
// isn't load-bearing data until M4/M9 land. Do not synthesize placeholder
// values here to make the fields "look" populated.
func attachAVCConsensusFields(block *config.ZKBlock) {
	block.Slot = messaging.LiveSlotFor(block.BlockNumber)
	block.Period = messaging.DefaultPeriodStore.PeriodFor(block.BlockNumber)

	if Security.M2bHashEnabled {
		block.BlockHash = Security.RecomputeBlockHashWithConsensusFields(block)
	}
}
