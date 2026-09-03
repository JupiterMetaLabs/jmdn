package messaging

// The single entry point for a block's entropy side effects, so the live path
// and the sync path cannot drift apart.
//
// Before this file the sequence below was written out twice — in broadcast.go's
// ProcessBlockLocally and blockPropagation.go's receiver twin — and a THIRD
// path, thebesync's applyBlock, did none of it. A node that caught up through
// sync therefore held no aggregate for any slot it synced, so every epoch that
// fell back during the catch-up failed closed on that node while its peers
// resolved normally. One definition, one store, one order.

import "gossipnode/config"

// ApplyBlockEntropyEffects folds one COMMITTED block into this node's entropy
// state. Call from every live block-application path, after the block is
// stored and the slot counter has advanced.
//
// The order is load-bearing:
//
//  1. foldBlockDeclaredReveals — this block's own reveals must be in the
//     accumulator before any epoch it closes is finalised.
//  2. VerifyAndRecordPrevCert — a window slot recorded by this block must be
//     available to an epoch this same block finalises.
//  3. maybeFinaliseCompletedEpochs — closes epochs, populates the mix store,
//     and triggers Stage-E sealing.
//  4. VerifyAndAcceptVDFProof — LAST, so it can use a mix step 3 may have just
//     produced.
func ApplyBlockEntropyEffects(block *config.ZKBlock) {
	foldBlockDeclaredReveals(block)
	VerifyAndRecordPrevCert(block)
	maybeFinaliseCompletedEpochs(block)
	_ = VerifyAndAcceptVDFProof(block)
}

// RecordSyncedBlockEntropy folds one block applied through SYNC (thebesync,
// fast sync, replay) into the same entropy state.
//
// It runs steps 1, 2 and 4 of ApplyBlockEntropyEffects and DELIBERATELY OMITS
// step 3, epoch finalisation.
//
// WHY FINALISATION IS OMITTED HERE. maybeFinaliseCompletedEpochs notifies
// Stage E, which starts a background VDF evaluation per finalised epoch
// (~T_vdf of sequential squaring each). A catch-up replaying thousands of
// blocks crosses hundreds of epoch boundaries, and finalising during replay
// would launch hundreds of concurrent evaluations for epochs whose entropy is
// long past useful — retention keeps only a handful. Historical epochs do not
// need re-finalising; what the node needs is the AGGREGATE STATE, which steps
// 1 and 2 rebuild, so that the first epoch it participates in live can fall
// back correctly.
//
// The consequence is explicit rather than hidden: while syncing, step 4 will
// usually report ErrMixUnavailable, because a mix exists only for an epoch
// this node finalised. That is the correct fail-closed answer — it declines to
// adopt a proof it cannot independently verify — and it resolves once the node
// is live and finalising its own epochs.
func RecordSyncedBlockEntropy(block *config.ZKBlock) {
	foldBlockDeclaredReveals(block)
	VerifyAndRecordPrevCert(block)
	_ = VerifyAndAcceptVDFProof(block)
}
