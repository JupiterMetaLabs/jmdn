package Sequencer

// VDFSealer runs the epoch VDF evaluation on a background goroutine, per
// AVC-Low-Level-Design.md §1. Sealing takes ~T_vdf (target 1200-1410s) of
// unavoidable sequential computation; doing it synchronously would stall
// block commits for most of the epoch. This is deliberately isolated from the
// block-commit loop - nothing here touches Consensus.Start, matching the
// design doc's own "no consensus-flow change" instruction.
//
// NOT WIRED: the trigger call (Start, meant to fire once the entropy
// committee's reveal window closes and the mix is folded - Low-Level-Design
// §1's "on mix-ready") and the read (Result, at epoch-boundary block-build
// time) have no caller in Consensus.go yet. The entropy-committee reveal
// collection this depends on (Low-Level-Design §4, M4.1-M4.4) is not built
// either. This type is ready for that caller once it lands - see
// AVC-Low-Level-Design.md §8's build-order note: the VDF itself has zero
// dependency on anything still open, so it can be built now and wired later.

import (
	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"
)

// SealResult is what the background goroutine reports back.
type SealResult struct {
	ForEpoch uint64
	Proof    vdf.Proof
	Err      error
}

// VDFSealer wraps one epoch's VDF evaluation. Create a fresh VDFSealer per
// epoch - Start must be called at most once per instance, matching the
// single-buffered result channel.
type VDFSealer struct {
	pipeline *beacon.Pipeline
	resultCh chan SealResult
}

// NewVDFSealer wraps the network's beacon pipeline for one epoch's sealing.
func NewVDFSealer(pipeline *beacon.Pipeline) *VDFSealer {
	return &VDFSealer{pipeline: pipeline, resultCh: make(chan SealResult, 1)}
}

// Start launches the sealing goroutine for forEpoch against mix and returns
// immediately - the ~20 minute evaluation runs in the background, and the
// caller's block-commit loop keeps committing normally for slots K+1..N-1 in
// the meantime.
//
// Uses Pipeline.Seal, not SealLocally: the caller needs the actual vdf.Proof
// bytes to embed in the epoch-boundary block (config.ZKBlock.VdfProof), and
// SealLocally discards them - it's the recovery path for a node that only
// needs to publish entropy, not carry the proof forward.
func (s *VDFSealer) Start(forEpoch uint64, mix randao.Seed) {
	go func() {
		proof, err := s.pipeline.Seal(forEpoch, mix)
		s.resultCh <- SealResult{ForEpoch: forEpoch, Proof: proof, Err: err}
	}()
}

// Result returns immediately, ready or not - it never blocks waiting for the
// goroutine. A node building the epoch-boundary block calls this once; if the
// proof isn't ready yet, ok is false and the caller must fail closed (the
// existing ErrEntropyUnavailable pattern), never guess or wait past its own
// slot deadline. The channel is single-shot: once drained, a second Result
// call also reports not-ready, even after the goroutine has long finished -
// callers that need the value again should have kept it from the first call.
func (s *VDFSealer) Result() (SealResult, bool) {
	select {
	case r := <-s.resultCh:
		return r, true
	default:
		return SealResult{}, false
	}
}
