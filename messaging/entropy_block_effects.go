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
	if block == nil {
		return
	}
	entropyEffectsMu.Lock()
	defer entropyEffectsMu.Unlock()
	applyEntropyEffectsLocked(block, entropyModeLive)
}

// entropyMode selects which of the steps a block application runs.
type entropyMode int

const (
	entropyModeLive   entropyMode = iota // gossip / local commit
	entropyModeSync                      // thebesync catch-up
	entropyModeReplay                    // startup replay of stored blocks
)

// applyEntropyEffectsLocked is the one body behind every entry point.
// Caller holds entropyEffectsMu.
func applyEntropyEffectsLocked(block *config.ZKBlock, mode entropyMode) {
	noteCommittedSlot(block.Slot)

	foldBlockDeclaredReveals(block)
	VerifyAndRecordPrevCert(block)

	if mode == entropyModeLive {
		maybeFinaliseCompletedEpochs(block)
	} else {
		// Sync and replay decide epochs exactly as the live path does (same
		// function, same order), but the Stage-E hook is suppressed: a
		// catch-up crossing many epochs must not launch one VDF evaluation
		// per epoch. Sealing for the newest epoch is resumed once, after the
		// replay (resumeSealingAfterReplay), or by the next live decision.
		wasQuiet := entropyQuietFinalise.Swap(true)
		maybeFinaliseCompletedEpochs(block)
		entropyQuietFinalise.Store(wasQuiet)
	}

	_ = VerifyAndAcceptVDFProof(block)

	// 5. Recovery deadline. LIVE PATH ONLY — see maybeTriggerVDFProofRecovery
	//    for why the sync path must not do this. Does no I/O: it reads
	//    in-memory state and at most hands an epoch to a background
	//    dispatcher, so block processing never waits on a peer.
	if mode == entropyModeLive {
		maybeTriggerVDFProofRecovery(block)
	}
}

// RecordSyncedBlockEntropy folds one block applied through SYNC (thebesync,
// fast sync, replay) into the same entropy state.
//
// It runs the same steps as ApplyBlockEntropyEffects, INCLUDING epoch
// finalisation, but with the Stage-E sealing hook suppressed and without the
// VDF-recovery deadline.
//
// WHY FINALISATION NOW RUNS HERE. It used to be omitted, to avoid launching a
// VDF evaluation per epoch crossed during catch-up. That left a synced node
// with no mix for any epoch it caught up through: it could not verify the
// boundary block's proof (ErrMixUnavailable), so it held no ENTROPY for the
// next epoch, could not build that epoch's accumulator and dropped its
// reveals. Deciding in quiet mode keeps the mixes (and makes proof adoption
// work during catch-up) while still starting no evaluations. Decisions use the
// committed blocks only, so a synced node reaches the same mix as the fleet.
func RecordSyncedBlockEntropy(block *config.ZKBlock) {
	if block == nil {
		return
	}
	entropyEffectsMu.Lock()
	defer entropyEffectsMu.Unlock()
	applyEntropyEffectsLocked(block, entropyModeSync)
}
