package messaging

// Stage D of the M4 pipeline — REWRITTEN 2026-08-20 for two changes:
// the cutoff-slot trigger (M4-1) and the fail-closed fallback selection.
//
// # Change 1 — Finalise() now runs at the CUTOFF SLOT, not at epoch rollover
//
// The previous version finalised epoch E when a block belonging to epoch E+1
// arrived (epochsNewlyClosedBy). That is the epoch BOUNDARY — slot (E+1)*N —
// and it is too late. Architecture §4.5/§7.2 require Finalise() at slot
// E*N+K, K slots into the epoch, WHILE epoch E is still running. The whole
// reason the reveal window is short is to leave the remaining N-K slots as VDF
// runway to seal ENTROPY-(E+1) before epoch E+1 needs a committee. Finalising
// at rollover left that runway at zero and would have stalled the beacon
// exactly when the next epoch needed it.
//
// epochsWithClosedRevealWindow replaces epochsNewlyClosedBy accordingly: it
// keys off the current SLOT against each epoch's cutoff slot, not off epoch
// numbers.
//
// # Change 2 — the fallback branch is now fail-closed, in a stated order
//
// The previous version silently substituted a documented-insecure interim
// state-root formula into every fallback outcome, making a grindable seed the
// DEFAULT with no operator decision. The selection is now explicit:
//
//  1. Architecture §4.2a's aggregate-signature fold over [K, K+B) — the target
//     design. Blocked today by B1 (aggSig is never persisted), so it always
//     errors; see entropy_fallback_window.go.
//  2. Otherwise: no seed. The epoch does not finalise, nothing is published,
//     and the failure is logged. A missing beacon value is a visible,
//     fail-closed halt; a wrong one silently steers every subsequent committee.
//
// REMOVED 2026-08-20: an interim state-root formula used to sit between those
// two as an opt-in escape hatch. It was deleted, along with the state-root
// tracking that fed it, because it was grindable by the proposer (transaction
// selection changes the state root) and — with reveals not yet flowing on a
// live network — it would have been the seed source for effectively every
// epoch rather than a rare degraded path. There is now exactly one fallback
// formula, and no way to reach a weaker one by setting an env var.
//
// randao.Fallback() — §4.2a's RESOLVED-AS-BROKEN offline-precomputable
// formula — is never returned by any of these paths. Accumulator.Finalise()
// still computes it internally for the fallback branch, and every path below
// discards that value.
//
// # Amendment, 2026-08-24 — two-phase finalisation
//
// The single-shot design above had a bug found while reviewing a count-based
// alternative to the fixed [K,K+B) fold window: maybeFinaliseCompletedEpochs
// called the fallback fold (finaliseEpoch -> resolveFallbackSeed) AT the
// cutoff slot itself, which is also the instant the collection range OPENS.
// Zero signers exist in the range at that moment under any window design, so
// the fallback path could never succeed — every fallback epoch failed closed
// unconditionally, which is a stronger and unintended failure than "fail
// closed when genuinely unable to compute a seed."
//
// Finalisation is now two phases: decideEpoch makes only the mixed-vs-fallback
// call at the cutoff (a mixed outcome still finalises immediately). A
// fallback outcome enters pendingFallback and is retried by
// resolvePendingFallbacks on every subsequently committed block, until either
// enough signers have been collected (see entropy_fallback_window.go's
// FallbackFoldBufferB) or the collection deadline passes (see
// FallbackFoldMaxSlotOffset) — see this file's "Two-phase finalisation"
// section below. resolveFallbackSeed and finaliseEpoch (the single-shot
// functions) are removed; decideEpoch and resolvePendingFallbacks replace
// them.
//
// # The genesis gap — unchanged, still disclosed, still not solved here
//
// This cannot produce a result for the network's first epoch: it needs
// SelectEntropyCommittee, which needs beacon.EpochEntropy(epoch), and nothing
// publishes a value before some epoch's Seal() has run. No genesis/bootstrap
// entropy exists anywhere in this codebase. Every call fails closed until that
// is decided — logged, never fatal, never guessed around.
import (
	"errors"
	"sort"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// ---------------------------------------------------------------------------
// Stage D -> Stage E seam
// ---------------------------------------------------------------------------

// epochFinalisedHook, when set, is invoked exactly once per epoch, the moment
// that epoch's Accumulator has been finalised — with the epoch that just
// closed and the seed it finalised to.
//
// This is the seam to Stage E (VDF sealing), which lives in package Sequencer —
// a package that already imports messaging, so messaging cannot import it back.
// Sequencer registers itself here at startup instead.
//
// Nil is normal until then, and a nil hook is a silent no-op rather than an
// error: a node without Stage E/F installed must keep finalising epochs.
// Sealing is allowed to be missing; finalisation is not.
var (
	epochFinalisedHookMu sync.Mutex
	epochFinalisedHook   func(closedEpoch uint64, seed randao.Seed)
)

// SetEpochFinalisedHook installs the Stage-E callback. Call once at startup.
func SetEpochFinalisedHook(f func(closedEpoch uint64, seed randao.Seed)) {
	epochFinalisedHookMu.Lock()
	epochFinalisedHook = f
	epochFinalisedHookMu.Unlock()
}

func notifyEpochFinalised(closedEpoch uint64, seed randao.Seed) {
	epochFinalisedHookMu.Lock()
	hook := epochFinalisedHook
	epochFinalisedHookMu.Unlock()
	if hook != nil {
		hook(closedEpoch, seed)
	}
}

// ---------------------------------------------------------------------------
// Two-phase finalisation — the security- and liveness-sensitive part of this
// file. See this file's header amendment for why this replaced a single-shot
// design.
// ---------------------------------------------------------------------------

// decideEpoch makes the one decision Architecture §7.2 ties to the cutoff
// slot: mixed, or fallback. A mixed outcome finalises immediately, exactly as
// the single-shot design did. A fallback outcome is marked pending — it must
// NOT try to resolve a seed here, because the collection range has just
// opened and holds zero signers at this exact instant.
func decideEpoch(epoch uint64, block *config.ZKBlock) {
	acc, err := entropyAccumulatorFor(epoch)
	if err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).Uint64("height", block.BlockNumber).
			Msg("entropy: cannot decide epoch outcome — no accumulator")
		return
	}

	res := acc.Finalise()
	if res.Outcome != randao.OutcomeFallback {
		notifyEpochFinalised(epoch, res.Seed)
		pruneAggSigsBelow(cutoffSlotFor(epoch))
		pruneRevealsBelow(epoch + 1)
		return
	}

	// Every path past this point discards randao.Fallback()'s output, which
	// Accumulator.Finalise() has already put in res.Seed — that is §4.2a's
	// RESOLVED-AS-BROKEN formula and it must never reach the beacon. Nothing
	// below reads res.Seed again; the eventual seed comes only from
	// resolvePendingFallbacks once collection succeeds.
	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()
	log.Info().Uint64("epoch", epoch).Strs("withheld", res.Withheld).Uint64("height", block.BlockNumber).
		Msg("entropy: epoch entered fallback at the reveal cutoff — collecting aggregate-signature signers before it can finalise")
}

// resolvePendingFallbacks re-attempts every epoch still waiting on the
// aggregate-signature fold, at this block's slot. Called on every committed
// block (not just at the cutoff) via maybeFinaliseCompletedEpochs, because a
// pending epoch's signers are exactly the blocks committed after its cutoff —
// they do not exist yet when decideEpoch runs.
func resolvePendingFallbacks(block *config.ZKBlock) {
	finaliseTrackMu.Lock()
	pending := make([]uint64, 0, len(pendingFallback))
	for e := range pendingFallback {
		pending = append(pending, e)
	}
	finaliseTrackMu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })

	for _, e := range pending {
		seed, err := FallbackSeedForEpoch(e, block.Slot)
		switch {
		case err == nil:
			finaliseTrackMu.Lock()
			delete(pendingFallback, e)
			finaliseTrackMu.Unlock()
			log.Info().Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: epoch finalised via the §4.2a aggregate-signature fallback")
			notifyEpochFinalised(e, seed)
			pruneAggSigsBelow(cutoffSlotFor(e))
			pruneRevealsBelow(e + 1)
		case errors.Is(err, ErrFallbackNotYetReady):
			// Still collecting; try again on the next block.
		case errors.Is(err, ErrFallbackDeadlineExceeded):
			finaliseTrackMu.Lock()
			delete(pendingFallback, e)
			finaliseTrackMu.Unlock()
			log.Error().Err(err).Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: fallback deadline exceeded — no seed produced for this epoch (fail closed by design; not retried again)")
			pruneAggSigsBelow(cutoffSlotFor(e))
			pruneRevealsBelow(e + 1)
		default:
			log.Error().Err(err).Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: unexpected error resolving a pending fallback epoch")
		}
	}
}

// ---------------------------------------------------------------------------
// Cutoff-slot trigger
// ---------------------------------------------------------------------------

// cutoffSlotFor returns the slot at which epoch's reveal window closes and
// Finalise() must run for it: E*N + K (Architecture §7.2).
func cutoffSlotFor(epoch uint64) uint64 { return epoch*N + RevealCutoffK }

// epochsWithClosedRevealWindow returns, in ascending order, every epoch whose
// cutoff slot has been reached at currentSlot and that has not yet been
// finalised.
//
// This is the M4-1 fix. The previous helper (epochsNewlyClosedBy) took the
// current EPOCH and returned everything strictly before it — i.e. it finalised
// epoch E only once epoch E+1 had started, at slot (E+1)*N. This takes the
// current SLOT and fires at E*N+K instead, N-K slots earlier, which is what
// gives the VDF its runway.
//
// Note the consequence, which is intended: an epoch is finalised while it is
// still running. Reveals arriving after its cutoff are correctly ignored —
// that is what the cutoff means.
//
// Pure and side-effect-free so the trigger boundary can be tested directly.
func epochsWithClosedRevealWindow(currentSlot, lastFinalisedEpoch uint64, haveFinalisedAny bool) []uint64 {
	start := uint64(0)
	if haveFinalisedAny {
		start = lastFinalisedEpoch + 1
	}
	if currentSlot < RevealCutoffK {
		return nil
	}
	// cutoffSlotFor(e) = e*N + K <= currentSlot  =>  e <= (currentSlot-K)/N.
	// Computing the bound directly rather than looping to it keeps this O(1)
	// in the common case and impossible to run away.
	last := (currentSlot - RevealCutoffK) / N
	if last < start {
		return nil
	}
	out := make([]uint64, 0, last-start+1)
	for e := start; e <= last; e++ {
		out = append(out, e)
	}
	return out
}

// finaliseTrackMu guards all three of the maps/counters below — the decided-
// epoch watermark and the set of epochs still waiting on their fallback
// fold. One mutex for all three because decideEpoch and
// resolvePendingFallbacks are always called back-to-back from the same
// commit hook and never need fine-grained locking between them.
var (
	finaliseTrackMu  sync.Mutex
	lastDecidedEpoch uint64
	haveDecidedAny   bool
	pendingFallback  = make(map[uint64]struct{})
)

// maybeFinaliseCompletedEpochs runs both phases for a newly committed block:
// decide any epoch whose reveal cutoff this block's slot has just reached,
// then retry every still-pending fallback epoch against this block.
//
// Call once per committed block, from the same two hooks
// foldBlockDeclaredReveals uses (broadcast.go's ProcessBlockLocally,
// blockPropagation.go's receive path) — and AFTER foldBlockDeclaredReveals and
// VerifyAndRecordPrevCert, so this block's own reveal and its parent's
// certificate are in before either phase runs.
//
// A mixed epoch, or a fallback epoch that resolves (succeeds or hits its
// deadline), is never revisited: decideEpoch advances lastDecidedEpoch
// unconditionally, and resolvePendingFallbacks removes an epoch from
// pendingFallback the moment it resolves either way. Retrying a resolved
// epoch would mean folding signers from slots the cutoff or the deadline has
// already ruled out — precisely what both boundaries exist to prevent.
func maybeFinaliseCompletedEpochs(block *config.ZKBlock) {
	finaliseTrackMu.Lock()
	toDecide := epochsWithClosedRevealWindow(block.Slot, lastDecidedEpoch, haveDecidedAny)
	finaliseTrackMu.Unlock()

	for _, e := range toDecide {
		decideEpoch(e, block)
		finaliseTrackMu.Lock()
		lastDecidedEpoch = e
		haveDecidedAny = true
		finaliseTrackMu.Unlock()
	}

	resolvePendingFallbacks(block)

	// Committee-snapshot anchoring (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md
	// items 1/8) - piggybacks on this function's existing per-commit call
	// sites (blockPropagation.go, broadcast.go) rather than adding a third.
	// No-op unless CommitteeSnapshotAnchorEnabled is on.
	maybeFreezeUpcomingSnapshot(block.Slot)
}
