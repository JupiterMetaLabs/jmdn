package Sequencer

// Stage E of the M4 pipeline (AVC-M4-Entropy-Reveal-Pipeline-Design.md §E) —
// wires messaging's Stage-D "epoch just finalised" event into
// VDFSealer.Start, per Low-Level-Design §1's "on mix-ready" trigger
// (VDF-Implementation-Handoff.md §5's snippet). vdf_sealer.go's own header
// comment says this trigger call "has no caller in Consensus.go yet" — this
// file is that caller.
//
// The trigger arrives via messaging.SetEpochFinalisedHook rather than a
// direct call, because messaging cannot import Sequencer: Sequencer already
// imports messaging (consensus_statemachine.go's
// messaging.BroadcastBlockToEveryNode* calls), so the reverse import would
// cycle. InstallEpochFinalisedHook registers this file's callback with that
// seam; call it once at startup (InstallAVCBeaconFromEnv, Stage F, does
// this for you as part of installing the beacon).
//
// STILL NOT LIVE ON ITS OWN: onEpochFinalised only starts sealing once a
// real *beacon.Pipeline has been installed via SetVDFPipeline, which only
// happens once Stage F's two crypto parameters (a provenance-verified VDF
// group modulus, a fleet-calibrated difficulty T) are actually supplied —
// see beacon_install.go's header for exactly why those are not, and must
// not be, invented here. Until then, onEpochFinalised logs and returns
// without starting anything — a mix is computed (Stage D succeeded) but
// never gets sealed or published.
import (
	"errors"
	"sync"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/messaging"
)

// ErrVDFProofNotReady is returned by Block.attachAVCConsensusFields (via
// SealerResultFor) when an epoch-boundary block is being built but that
// epoch's VDF sealing has not finished yet. Callers MUST fail closed on it —
// same discipline as committee.ErrEntropyUnavailable: proposing a
// boundary block with a missing/zero VdfProof would let whichever node
// happens to have raced ahead choose the committee for every node that
// didn't.
var ErrVDFProofNotReady = errors.New("Sequencer: VDF proof for this epoch's boundary block is not ready yet (fail closed)")

var (
	vdfPipelineMu sync.Mutex
	vdfPipeline   *beacon.Pipeline
)

// SetVDFPipeline installs the network's beacon pipeline. Call once at
// startup — never with a nil group, a zero difficulty, or a modulus this
// process generated itself; beacon.New already fails closed on the first
// two, and avc/vdf.NewRSAGroup's own doc comment explains why the third can
// silently produce a VDF with no real delay, undetectably.
func SetVDFPipeline(p *beacon.Pipeline) {
	vdfPipelineMu.Lock()
	vdfPipeline = p
	vdfPipelineMu.Unlock()
}

func activeVDFPipeline() *beacon.Pipeline {
	vdfPipelineMu.Lock()
	defer vdfPipelineMu.Unlock()
	return vdfPipeline
}

var (
	vdfSealersMu sync.Mutex
	vdfSealers   = make(map[uint64]*VDFSealer)
)

// InstallEpochFinalisedHook registers this file's sealing trigger with
// messaging's Stage-D seam. Call once at startup, any time relative to
// SetVDFPipeline — onEpochFinalised reads activeVDFPipeline() fresh on every
// invocation rather than capturing it at registration time, so the two
// calls' order doesn't matter.
func InstallEpochFinalisedHook() {
	messaging.SetEpochFinalisedHook(onEpochFinalised)
}

// onEpochFinalised is messaging's Stage-D callback: closedEpoch's
// Accumulator was just finalised to seed. Starts sealing for
// closedEpoch+1 — beacon.Pipeline.Seal's own doc comment is explicit that
// forEpoch must be "the epoch AFTER the one whose reveals produced the
// mix", which is exactly closedEpoch+1 here.
func onEpochFinalised(closedEpoch uint64, seed randao.Seed) {
	forEpoch := closedEpoch + 1

	pipeline := activeVDFPipeline()
	if pipeline == nil {
		log.Warn().Uint64("closed_epoch", closedEpoch).Uint64("for_epoch", forEpoch).
			Msg("entropy: epoch finalised but no VDF pipeline installed yet (Stage F not wired) — mix computed, sealing skipped, entropy for this epoch will never be published")
		return
	}

	sealer := sealerFor(forEpoch, pipeline)
	sealer.Start(forEpoch, seed)
	log.Info().Uint64("closed_epoch", closedEpoch).Uint64("for_epoch", forEpoch).
		Msg("entropy: VDF sealing started in background (target ~1200-1410s, VDF-Implementation-Handoff.md §0) for the newly finalised epoch")
}

// sealerFor returns forEpoch's VDFSealer, constructing it on first use.
// VDFSealer.Start must be called at most once per instance (its own doc
// comment — single-buffered result channel); keying by forEpoch here is
// what makes that true across repeated/replayed onEpochFinalised calls.
func sealerFor(forEpoch uint64, pipeline *beacon.Pipeline) *VDFSealer {
	vdfSealersMu.Lock()
	defer vdfSealersMu.Unlock()
	if s, ok := vdfSealers[forEpoch]; ok {
		return s
	}
	s := NewVDFSealer(pipeline)
	vdfSealers[forEpoch] = s
	return s
}

// SealerResultFor returns forEpoch's sealing result, if a sealer was started
// for it and has finished. This is the read side of the
// VDF-Implementation-Handoff.md §5/§6 pattern. WIRED: Block/consensus_fields.go
// calls this on the epoch-boundary block to attach VdfProof, returning
// ErrVDFProofNotReady (fail closed) if ok is false.
func SealerResultFor(forEpoch uint64) (SealResult, bool) {
	vdfSealersMu.Lock()
	s, ok := vdfSealers[forEpoch]
	vdfSealersMu.Unlock()
	if !ok {
		return SealResult{}, false
	}
	return s.Result()
}

// SeedSealResultForTest injects a completed (or failed) SealResult for
// forEpoch directly into vdfSealers, bypassing Start and the background
// goroutine entirely. Test-only — lets a test in another package (e.g.
// Block/consensus_fields_test.go) exercise SealerResultFor's ok=true and
// ok=false paths without running a real ~20-minute VDF evaluation. The
// ok=false ("not ready") path needs no seam at all: simply don't seed a
// result for that epoch, and SealerResultFor already returns
// (SealResult{}, false) for any epoch with no registered sealer. Overwrites
// any sealer already registered for forEpoch.
func SeedSealResultForTest(forEpoch uint64, result SealResult) {
	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	s.resultCh <- result
	vdfSealersMu.Lock()
	vdfSealers[forEpoch] = s
	vdfSealersMu.Unlock()
}
