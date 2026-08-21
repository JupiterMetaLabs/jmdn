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
// # The genesis gap — unchanged, still disclosed, still not solved here
//
// This cannot produce a result for the network's first epoch: it needs
// SelectEntropyCommittee, which needs beacon.EpochEntropy(epoch), and nothing
// publishes a value before some epoch's Seal() has run. No genesis/bootstrap
// entropy exists anywhere in this codebase. Every call fails closed until that
// is decided — logged, never fatal, never guessed around.
import (
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
// Fallback selection — the security-sensitive part of this file
// ---------------------------------------------------------------------------

// fallbackSeedSource names which formula produced a fallback seed, so logs
// never have to infer it. One value today; kept as a named type because a
// future threshold-BLS variant (§10 decision 1f) would be a second one.
type fallbackSeedSource string

const fallbackSourceAggSig fallbackSeedSource = "aggsig-window" // §4.2a, the only formula

// resolveFallbackSeed picks the fallback seed for epoch: §4.2a's
// aggregate-signature fold, or nothing.
//
// Takes the fold as a parameter rather than reading global state so the
// fail-closed behaviour can be tested without a live committee, beacon, or
// aggregate store.
//
// There is deliberately no second option. An epoch that cannot compute the
// §4.2a seed does not finalise — see this file's header for why a visible halt
// beats a weaker seed.
func resolveFallbackSeed(
	epoch uint64,
	aggSigSeed func(uint64) (randao.Seed, error),
) (randao.Seed, fallbackSeedSource, error) {

	seed, err := aggSigSeed(epoch)
	if err != nil {
		return randao.Seed{}, "", err
	}
	return seed, fallbackSourceAggSig, nil
}

// ---------------------------------------------------------------------------
// Finalisation
// ---------------------------------------------------------------------------

// finaliseEpoch finalises epoch's Accumulator, replacing the fallback branch's
// seed per resolveFallbackSeed. A mixed outcome passes through untouched.
func finaliseEpoch(epoch uint64) (randao.Result, error) {
	acc, err := entropyAccumulatorFor(epoch)
	if err != nil {
		return randao.Result{}, err
	}

	res := acc.Finalise()
	if res.Outcome != randao.OutcomeFallback {
		return res, nil
	}

	// Every path from here discards randao.Fallback()'s output, which
	// Accumulator.Finalise() has already put in res.Seed — that is §4.2a's
	// RESOLVED-AS-BROKEN formula and it must never reach the beacon.
	seed, source, err := resolveFallbackSeed(epoch, FallbackSeedForEpoch)
	if err != nil {
		return randao.Result{}, err
	}

	res.Seed = seed
	log.Info().Uint64("epoch", epoch).Strs("withheld", res.Withheld).
		Str("formula", string(source)).
		Msg("entropy: epoch finalised via the §4.2a aggregate-signature fallback")
	return res, nil
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

var (
	finaliseTrackMu    sync.Mutex
	lastFinalisedEpoch uint64
	haveFinalisedAny   bool
)

// maybeFinaliseCompletedEpochs finalises every epoch whose reveal cutoff this
// block's slot has reached, and notifies Stage E for each one that succeeds.
//
// Call once per committed block, from the same two hooks
// foldBlockDeclaredReveals uses (broadcast.go's ProcessBlockLocally,
// blockPropagation.go's receive path) — and AFTER foldBlockDeclaredReveals and
// VerifyAndRecordPrevCert, so this block's own reveal and its parent's
// certificate are in before any epoch is finalised using them.
//
// An epoch that fails to finalise is still marked handled and never retried:
// its reveal window really has closed, whether or not the failure is fixed
// later. Retrying would mean folding reveals that arrived after the cutoff,
// which is precisely what the cutoff exists to exclude.
func maybeFinaliseCompletedEpochs(block *config.ZKBlock) {
	finaliseTrackMu.Lock()
	toClose := epochsWithClosedRevealWindow(block.Slot, lastFinalisedEpoch, haveFinalisedAny)
	finaliseTrackMu.Unlock()

	for _, e := range toClose {
		res, err := finaliseEpoch(e)

		finaliseTrackMu.Lock()
		lastFinalisedEpoch = e
		haveFinalisedAny = true
		finaliseTrackMu.Unlock()

		if err != nil {
			log.Error().Err(err).Uint64("epoch", e).
				Uint64("height", block.BlockNumber).Uint64("slot", block.Slot).
				Msg("entropy: reveal window closed but the epoch could not be finalised — no seed produced, nothing downstream can seat off this epoch (fail closed by design; see this file's fallback-selection order)")
			continue
		}

		notifyEpochFinalised(e, res.Seed)

		// The next epoch's fold window starts strictly after this one's, so
		// anything below this epoch's cutoff can never be needed again.
		pruneAggSigsBelow(cutoffSlotFor(e))

		// Same for buffered reveals: this epoch's window is closed, so nothing
		// still held for it (or for any earlier epoch) can ever be included.
		pruneRevealsBelow(e + 1)
	}
}
