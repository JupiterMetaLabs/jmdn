package messaging

// Inbound VDF proof adoption — the receive-side twin of the proposer's
// VdfProof attachment (Block/consensus_fields.go).
//
// # The gap this closes
//
// config.ZKBlock.VdfProof rode the wire, was folded into the M2b
// ConsensusHash, and was persisted to extra_data — and was then used for
// nothing. beacon.Pipeline.Accept, the millisecond verify-and-publish path,
// had zero callers anywhere in this repository. Every non-proposing node
// therefore had to evaluate the VDF itself (~T_vdf of sequential squaring),
// which is the exact cost the field was added to avoid; the comment at
// DB_OPs/backend/block.go:196 says so.
//
// There was also no validation of a received proof of ANY kind — not group,
// not difficulty, not epoch. Nothing inside vdf.Proof names an epoch, so a
// proof legitimately sealed for epoch E would have published under whatever
// SeedEpoch a proposer declared.
//
// # Why this is a seam rather than a direct call
//
// The *beacon.Pipeline lives in package Sequencer, which already imports
// messaging, so messaging cannot import it back. Same constraint and same
// solution as SetEpochFinalisedHook: Sequencer registers an acceptor at
// startup (Sequencer.InstallVDFProofAcceptor, called from
// InstallAVCBeaconFromEnv) and this file calls whatever is registered.
//
// # Fail-closed discipline
//
// Pipeline.Accept's own doc states the rule implemented here: "A node that
// cannot verify must NOT fall back to trusting the claimed output." Every
// rejection path publishes NOTHING.
//
// A bad proof does not reject the BLOCK. The proof is covered by the
// ConsensusHash, so a relay cannot forge one — a bad proof is a proposer
// fault, and halting the chain on it would convert an entropy problem into a
// liveness problem. The block is accepted and contributes no entropy; the node
// recovers the epoch by another route (a later valid proof, or its own
// sealing).

import (
	"errors"
	"fmt"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// VDFProofAcceptor verifies proofBytes against mix for forEpoch and, on
// success, publishes the attested entropy into this node's BeaconSource.
// Implemented in package Sequencer over *beacon.Pipeline.
type VDFProofAcceptor func(forEpoch uint64, mix randao.Seed, proofBytes []byte) error

var (
	vdfAcceptorMu sync.Mutex
	vdfAcceptor   VDFProofAcceptor
)

// SetVDFProofAcceptor installs the acceptor. Call once at startup, from the
// same place the beacon itself is installed.
func SetVDFProofAcceptor(f VDFProofAcceptor) {
	vdfAcceptorMu.Lock()
	vdfAcceptor = f
	vdfAcceptorMu.Unlock()
}

func activeVDFAcceptor() VDFProofAcceptor {
	vdfAcceptorMu.Lock()
	defer vdfAcceptorMu.Unlock()
	return vdfAcceptor
}

// SealerCanceller stops a local in-flight VDF evaluation for an epoch.
// Implemented in package Sequencer (which owns vdfSealers) and registered here
// for the same import-cycle reason as VDFProofAcceptor.
type SealerCanceller func(forEpoch uint64)

var (
	sealerCancellerMu sync.Mutex
	sealerCanceller   SealerCanceller
)

// SetSealerCanceller installs the canceller. Call once at startup, alongside
// SetVDFProofAcceptor.
func SetSealerCanceller(f SealerCanceller) {
	sealerCancellerMu.Lock()
	sealerCanceller = f
	sealerCancellerMu.Unlock()
}

func activeSealerCanceller() SealerCanceller {
	sealerCancellerMu.Lock()
	defer sealerCancellerMu.Unlock()
	return sealerCanceller
}

var (
	// ErrProofNotOnBoundary — a proof arrived on a block that is not its
	// epoch's boundary slot. Only the boundary block may carry one.
	ErrProofNotOnBoundary = errors.New("messaging: VdfProof on a non-boundary block (rejected)")

	// ErrProofEpochMismatch — the block's declared SeedEpoch is not the epoch
	// its own slot belongs to. This is the wrong-epoch replay check.
	ErrProofEpochMismatch = errors.New("messaging: VdfProof SeedEpoch does not match the block's slot epoch (rejected)")

	// ErrMixUnavailable — this node never finalised the predecessor epoch, so
	// it holds no independent mix to verify against. Not a rejection of the
	// proof: a statement that this node cannot judge it.
	ErrMixUnavailable = errors.New("messaging: no locally-finalised mix for the predecessor epoch (cannot verify)")

	// ErrNoVDFAcceptor — Stage 2 is not installed on this node.
	ErrNoVDFAcceptor = errors.New("messaging: no VDF proof acceptor installed (Stage 1)")
)

// VerifyAndAcceptVDFProof consumes an epoch-boundary block's VdfProof.
//
// Idempotent: adopting the same valid proof twice is a no-op, because
// committee.BeaconSource.Publish is idempotent for an identical value and
// refuses a conflicting one. Safe to call from every block-application path —
// live gossip, local commit, and sync replay all route through here.
//
// Returns nil when there is nothing to do (no proof on this block, or already
// published). Errors are for observability and tests; callers on the block
// path log and continue, because none of these conditions justifies rejecting
// a block that already carries a valid committee certificate.
func VerifyAndAcceptVDFProof(block *config.ZKBlock) error {
	if block == nil || len(block.VdfProof) == 0 {
		return nil
	}

	accept := activeVDFAcceptor()
	if accept == nil {
		// Stage 1. Not an error: the field is advisory until a beacon exists.
		return ErrNoVDFAcceptor
	}

	declaredEpoch := block.SeedEpoch

	// CHECK 1 — the proof must sit on its epoch's boundary slot. Off-boundary
	// blocks leave VdfProof zero by design (Block/consensus_fields.go), so a
	// populated field anywhere else is malformed, not merely unexpected.
	if block.Slot != EpochBoundarySlot(declaredEpoch) {
		log.Error().Uint64("height", block.BlockNumber).Uint64("slot", block.Slot).
			Uint64("declared_epoch", declaredEpoch).
			Uint64("boundary_slot", EpochBoundarySlot(declaredEpoch)).
			Msg("entropy: rejecting VdfProof — not on the declared epoch's boundary slot")
		return fmt.Errorf("%w: block %d slot %d, epoch %d boundary is %d",
			ErrProofNotOnBoundary, block.BlockNumber, block.Slot, declaredEpoch, EpochBoundarySlot(declaredEpoch))
	}

	// CHECK 2 — wrong-epoch replay. SeedEpoch is proposer-declared; bind it to
	// the slot clock, which every node computes for itself.
	if got := EpochForSlot(block.Slot); got != declaredEpoch {
		log.Error().Uint64("height", block.BlockNumber).
			Uint64("declared_epoch", declaredEpoch).Uint64("slot_epoch", got).
			Msg("entropy: rejecting VdfProof — declared SeedEpoch does not match the slot's own epoch")
		return fmt.Errorf("%w: declared %d, slot %d is epoch %d",
			ErrProofEpochMismatch, declaredEpoch, block.Slot, got)
	}

	// CHECK 3 — the independent mix. Entropy for epoch E is sealed from the mix
	// epoch E-1 finalised to (beacon.Pipeline.Seal's one-epoch lag). The
	// verifier must hold that mix itself; see entropy_mix_store.go.
	if declaredEpoch == 0 {
		return fmt.Errorf("%w: epoch 0 has no predecessor (genesis bootstrap is an open item)", ErrMixUnavailable)
	}
	mix, ok := FinalisedMixFor(declaredEpoch - 1)
	if !ok {
		log.Warn().Uint64("height", block.BlockNumber).Uint64("for_epoch", declaredEpoch).
			Uint64("need_mix_of_epoch", declaredEpoch-1).
			Msg("entropy: cannot verify this block's VdfProof — this node never finalised the predecessor " +
				"epoch, so it holds no independent mix. NOT adopting. The node must recover this epoch " +
				"another way (a later block once caught up, or its own sealing) and must not continue on " +
				"Stage-1 salt for it")
		return fmt.Errorf("%w: need mix of epoch %d to verify epoch %d", ErrMixUnavailable, declaredEpoch-1, declaredEpoch)
	}

	// CHECKS 4 and 5 — group/difficulty binding and the VDF verification
	// itself, both inside Accept: it rejects proof.T != the pinned difficulty
	// and runs vdf.Verify, which re-derives the challenge from the group and
	// the mix. A proof from another group, another T, or over another mix
	// cannot verify here.
	if err := accept(declaredEpoch, mix, block.VdfProof); err != nil {
		log.Error().Err(err).Uint64("height", block.BlockNumber).Uint64("for_epoch", declaredEpoch).
			Msg("entropy: VdfProof REJECTED — no entropy published from it. The block itself is still " +
				"accepted (the proof is covered by the ConsensusHash, so this is a proposer fault, " +
				"not a relay one)")
		return err
	}

	// Durability. The adopted value is now in the in-memory beacon; persist it
	// so a restart does not lose an epoch that cannot be recomputed (the mix
	// ages out). Non-fatal: a KV failure degrades restart recovery, it must not
	// fail the block path.
	_ = PersistEpochEntropy(declaredEpoch)

	// Persist the PROOF too, so this node can serve this epoch to a peer that
	// is still recovering — without it, adoption helps only us. Uses exactly
	// the bytes that were just verified, so the stored encoding is the same one
	// the block carried and the same one the pull path will return.
	_ = PersistVDFProof(declaredEpoch, block.VdfProof)

	// FIRST VALID PROOF WINS. This node now holds epoch entropy, so any local
	// evaluation for the SAME epoch is redundant: on a T calibrated to minutes
	// that is the largest avoidable cost in the entropy path. Cancelling is
	// idempotent, so the duplicate proofs that arrive from other peers cost
	// nothing extra.
	//
	// Ordering is deliberate — cancel only AFTER Accept has succeeded and the
	// entropy is published. Cancelling first would risk abandoning local work
	// on the strength of a proof that then failed to publish.
	if cancel := activeSealerCanceller(); cancel != nil {
		cancel(declaredEpoch)
	}

	log.Info().Uint64("height", block.BlockNumber).Uint64("for_epoch", declaredEpoch).
		Msg("entropy: adopted a peer's VDF proof — epoch entropy published locally in milliseconds " +
			"instead of a full local evaluation")
	return nil
}
