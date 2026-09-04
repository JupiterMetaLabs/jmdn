package messaging

// Startup recovery for the fallback aggregate store.
//
// # The gap this closes
//
// defaultAggSigStore (entropy_fallback_window.go) is a plain in-memory map. A
// node that had collected 3 of the required B=5 aggregates and then restarted
// came back with an EMPTY store, so that epoch's fallback fold failed closed
// while peers that stayed up resolved it normally. The two nodes then held
// different entropy for the same epoch — the divergence the whole fail-closed
// discipline exists to make loud rather than silent.
//
// # Why this re-derives instead of restoring
//
// The aggregates were never the durable artefact; the CERTIFICATES were.
// DB_OPs writes extra_data["prev_agg_cert"] unconditionally on every block, so
// the inputs are already on disk. This function replays them through the SAME
// verifier a live node uses (VerifyAndRecordPrevCert), which re-checks every
// signature, re-checks eligibility and the quorum floor, and re-derives the
// aggregate locally.
//
// That matters: nothing new is trusted, there is no second serialisation
// format to keep in sync, and a corrupted or tampered record fails exactly
// where a live one would. Restoring a stored aggregate blob would have meant
// trusting bytes nobody re-verified.
//
// # Scope
//
// Only the ACTIVE fallback collection window is replayed — not all history.
// Aggregates outside it can never contribute to a fold (FallbackSeedForEpoch
// filters to [start, deadline)), so replaying more would be wasted reads.

import (
	"errors"
	"fmt"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// BlockByHeightFn loads one committed block by height. Injected so this file
// stays testable and does not hard-depend on a DB package.
type BlockByHeightFn func(height uint64) (*config.ZKBlock, error)

// maxRecoveryScanBlocks bounds the backward walk.
//
// The window is at most FallbackFoldMaxSlotOffset slots wide, and a block
// advances the slot counter by period+1 — so with timeouts, one slot can cost
// several heights. This cap keeps a pathological chain (or a corrupt slot
// field) from turning startup into an unbounded scan. Generous by design:
// exceeding it is a diagnostic, not a normal condition.
const maxRecoveryScanBlocks = 512

// RecoverAggSigStoreAtStartup replays committed blocks whose parent slots fall
// inside the active fallback collection window, rebuilding defaultAggSigStore.
//
// currentSlot is this node's recovered slot — call AFTER
// RecoverSlotStoreAtStartup, which is what makes that value trustworthy.
// tipHeight is the local committed tip.
//
// Returns the number of window slots recorded. A partial recovery is NOT an
// error: fewer than B aggregates simply means the fold will fail closed for
// that epoch, which is the correct, already-handled outcome. Errors are
// reserved for conditions that make the walk itself untrustworthy.
//
// No-op when the aggregate-certificate path is disabled — there is nothing to
// rebuild, and the store is never read.
func RecoverAggSigStoreAtStartup(currentSlot, tipHeight uint64, getBlock BlockByHeightFn) (recovered int, err error) {
	if !AggCertEnabled {
		return 0, nil
	}
	if getBlock == nil {
		return 0, errors.New("fallback recovery: nil block loader")
	}
	if tipHeight == 0 {
		// Genesis or unsynced — nothing committed to replay.
		return 0, nil
	}

	epoch := EpochForSlot(currentSlot)
	start, deadline, berr := randao.FallbackCollectionBounds(uint64(epoch), N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if berr != nil {
		return 0, fmt.Errorf("fallback recovery: computing collection bounds for epoch %d: %w", epoch, berr)
	}

	// Walk backwards from the tip. Each block carries its OWN slot, so the
	// slot->height mapping is read, never computed: AdvanceOnCommit does
	// `slot += period + 1`, so slots and heights are not 1:1 across a round
	// that timed out, and any arithmetic reconstruction would silently drift.
	scanned := 0
	for h := tipHeight; h > 0 && scanned < maxRecoveryScanBlocks; h-- {
		scanned++

		block, gerr := getBlock(h)
		if gerr != nil || block == nil {
			// A hole in local history is not fatal here: the fold fails closed
			// on a missing slot, which is the designed behaviour.
			log.Debug().Uint64("height", h).Msg("fallback recovery: block unreadable, skipping")
			continue
		}

		// VerifyAndRecordPrevCert records against the PARENT's slot
		// (block.Slot - (block.Period + 1)), so that is what decides whether
		// this block contributes to the window.
		if block.Slot == 0 || block.Slot < block.Period+1 {
			continue
		}
		prevSlot := block.Slot - (block.Period + 1)

		// Below the window and walking backwards — everything earlier is older
		// still, so stop rather than scan the rest of the chain.
		if prevSlot < start {
			break
		}
		if prevSlot >= deadline {
			continue // above the window; keep walking back
		}

		before := aggSigStoreLen()
		VerifyAndRecordPrevCert(block)
		if aggSigStoreLen() > before {
			recovered++
		}
	}

	if scanned >= maxRecoveryScanBlocks {
		log.Warn().Int("scanned", scanned).Uint64("tip_height", tipHeight).
			Msg("fallback recovery: hit the backward-scan cap before leaving the collection window — " +
				"recovery may be incomplete; check for a corrupt persisted slot field")
	}

	log.Info().
		Uint64("current_slot", currentSlot).Uint64("entropy_epoch", uint64(epoch)).
		Uint64("window_start", start).Uint64("window_deadline", deadline).
		Int("slots_recovered", recovered).Int("blocks_scanned", scanned).
		Uint64("required", FallbackFoldBufferB).
		Msg("fallback recovery: rebuilt the aggregate store for the active collection window from " +
			"persisted certificates")

	if uint64(recovered) < FallbackFoldBufferB {
		log.Warn().Int("slots_recovered", recovered).Uint64("required", FallbackFoldBufferB).
			Msg("fallback recovery: fewer window aggregates than a fold requires — if this epoch falls " +
				"back it will fail closed on this node until more in-window blocks commit. Expected " +
				"when the window has only just opened; investigate otherwise")
	}
	return recovered, nil
}

// aggSigStoreLen reports how many slots the store currently holds.
func aggSigStoreLen() int {
	defaultAggSigStore.mu.Lock()
	defer defaultAggSigStore.mu.Unlock()
	return len(defaultAggSigStore.sigs)
}

// AggSigStoreSlotsForTest returns the recorded slots. Test-only.
func AggSigStoreSlotsForTest() []uint64 {
	defaultAggSigStore.mu.Lock()
	defer defaultAggSigStore.mu.Unlock()
	out := make([]uint64, 0, len(defaultAggSigStore.sigs))
	for s := range defaultAggSigStore.sigs {
		out = append(out, s)
	}
	return out
}

// ResetAggSigStoreForTest clears the store. Test-only.
func ResetAggSigStoreForTest() {
	defaultAggSigStore.mu.Lock()
	defaultAggSigStore.sigs = make(map[uint64][]byte)
	defaultAggSigStore.mu.Unlock()
}
