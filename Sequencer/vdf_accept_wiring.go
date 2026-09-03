package Sequencer

// Stage-F receive side — registers the inbound VDF proof acceptor with
// messaging's seam (messaging.SetVDFProofAcceptor).
//
// The proposer half (attach a proof to the epoch-boundary block) was already
// wired in Block/consensus_fields.go. This is the half that was missing
// entirely: beacon.Pipeline.Accept — the millisecond verify-and-publish path,
// and the whole reason ZKBlock.VdfProof exists — had no caller anywhere, so
// every non-sealing node faced a full local evaluation or nothing.
//
// The dependency direction matches the Stage-D->E hook's, for the same reason:
// Sequencer imports messaging, so messaging cannot import Sequencer, and the
// *beacon.Pipeline lives here.

import (
	"errors"
	"fmt"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"

	"gossipnode/messaging"
)

// ErrNoPipelineForAccept is returned when a proof arrives but no pipeline is
// installed. Distinct from a bad proof: nothing is wrong with the proof, this
// node simply is not running Stage 2.
var ErrNoPipelineForAccept = errors.New("Sequencer: no VDF pipeline installed; cannot accept a peer's proof")

// InstallVDFProofAcceptor registers the acceptor. Called by
// InstallAVCBeaconFromEnv alongside SetBeaconSource / SetVDFPipeline /
// InstallEpochFinalisedHook, so the receive side can never be installed
// without the pipeline it needs.
//
// The closure reads activeVDFPipeline() fresh on every call rather than
// capturing it, matching onEpochFinalised, so registration order is irrelevant.
func InstallVDFProofAcceptor() {
	messaging.SetVDFProofAcceptor(func(forEpoch uint64, mix randao.Seed, proofBytes []byte) error {
		pipeline := activeVDFPipeline()
		if pipeline == nil {
			return ErrNoPipelineForAccept
		}

		var proof vdf.Proof
		if err := proof.UnmarshalBinary(proofBytes); err != nil {
			return fmt.Errorf("Sequencer: epoch %d proof does not decode: %w", forEpoch, err)
		}
		// Structural guard before the cryptographic one. UnmarshalBinary is
		// JSON-backed, so a proof carrying null Y/Pi decodes "successfully"
		// into nil big.Ints and would panic inside Verify's modular
		// arithmetic. Fail closed on shape before touching the maths.
		if proof.Y == nil || proof.Pi == nil {
			return fmt.Errorf("Sequencer: epoch %d proof decoded with a nil group element (Y or Pi)", forEpoch)
		}

		// Accept is the authority on the rest: it rejects proof.T != the
		// pinned difficulty, runs vdf.Verify (which re-derives the challenge
		// from the group and the mix, so a foreign group or a substituted mix
		// cannot verify), and publishes ONLY on success. Publishing is
		// idempotent for an identical value and refused for a conflicting one,
		// which is what makes repeated adoption of the same proof safe.
		return pipeline.Accept(forEpoch, mix, proof)
	})
}
