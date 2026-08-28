package messaging

// Committee-snapshot anchoring — docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md
// items 1, 6, 8. Freezes the eligible-validator snapshot for an UPCOMING
// epoch a little early (lookahead), caches its hash for the life of this
// process, and lets block assembly stamp that hash on-chain so a rejoining
// node can verify a snapshot body against something durable instead of
// trusting whoever served it.
//
// Gated OFF by default (JMDN_COMMITTEE_SNAPSHOT_ANCHOR) - same coordinated
// fleet-wide rollout pattern as JMDN_M2B_HASH and JMDN_AVC_AGG_CERT. With the
// flag off, maybeFreezeUpcomingSnapshot and frozenSnapshotHashFor are no-ops
// and attachAVCConsensusFields leaves the new ZKBlock field empty - today's
// behaviour, unchanged.
//
// SCOPE NOTE: this anchors the snapshot's HASH on-chain. It does not, by
// itself, make the snapshot BODY available to a node that has no local copy
// and no other source - that is the still-open "seed node serves
// GetCommitteeSnapshot" item, deliberately left in the TODO file rather than
// built here.

import (
	"sync"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/rs/zerolog/log"
)

// CommitteeSnapshotAnchorEnabled gates this feature. Default false.
var CommitteeSnapshotAnchorEnabled = envOn("JMDN_COMMITTEE_SNAPSHOT_ANCHOR", false)

// SnapshotFreezeLookahead is how many slots BEFORE an epoch begins its
// snapshot gets frozen, so every node has the same cached value ready before
// slot 0 of that epoch ever needs it - avoiding the race a freeze-at-the-
// exact-boundary design would have. Reuses RevealCutoffK rather than
// inventing a second lookahead constant; there is no requirement that the
// two be equal, only that this one be small relative to N and large enough
// for a freeze to propagate before it's needed - K already satisfies both by
// construction (Architecture §7.2 picked it for the same kind of margin).
const SnapshotFreezeLookahead = RevealCutoffK

var (
	frozenSnapshotHashMu sync.Mutex
	frozenSnapshotHash   = make(map[uint64][32]byte)
)

// frozenSnapshotHashFor returns the cached hash for epoch, if one has been
// frozen. ok=false covers both "not enabled" and "not reached the lookahead
// slot yet" - callers don't need to distinguish those, only "do I have a
// value to stamp."
func frozenSnapshotHashFor(epoch uint64) (h [32]byte, ok bool) {
	frozenSnapshotHashMu.Lock()
	defer frozenSnapshotHashMu.Unlock()
	h, ok = frozenSnapshotHash[epoch]
	return h, ok
}

// FrozenCommitteeSnapshotHashFor is frozenSnapshotHashFor's exported form,
// for Block/consensus_fields.go (a different package) to call when stamping
// a block under construction. Same semantics: ok=false means "nothing to
// stamp," never an error.
func FrozenCommitteeSnapshotHashFor(epoch uint64) (h [32]byte, ok bool) {
	return frozenSnapshotHashFor(epoch)
}

// maybeFreezeUpcomingSnapshot checks whether currentSlot has reached the
// freeze point for the NEXT epoch and, if so and not already frozen, builds
// that epoch's snapshot once and caches its hash for the rest of this
// process's life.
//
// Idempotent by construction: once an epoch's hash is cached, calling this
// again at any later slot in the same (or an earlier) epoch is a no-op for
// that epoch - the cache is checked before committeeSnapshotFor is ever
// called again, so a membership change between the freeze slot and the
// epoch's actual start can never retroactively alter the frozen value. That
// is the entire point of freezing.
//
// No-op entirely when CommitteeSnapshotAnchorEnabled is false.
func maybeFreezeUpcomingSnapshot(currentSlot uint64) {
	if !CommitteeSnapshotAnchorEnabled {
		return
	}
	upcoming := EpochForSlot(currentSlot) + 1
	freezeSlot := upcoming*N - SnapshotFreezeLookahead
	if currentSlot < freezeSlot {
		return
	}
	if _, already := frozenSnapshotHashFor(upcoming); already {
		return
	}

	snap, err := committeeSnapshotFor(upcoming)
	if err != nil {
		// No eligible source available yet, or it errored - stay unfrozen and
		// retry on the next call (e.g. the next committed block). Matches the
		// fail-safe-by-retry shape entropy_finalise.go's pending-fallback loop
		// already uses for the same reason: a transient failure here must not
		// permanently wedge this epoch's anchor.
		log.Warn().Err(err).Uint64("epoch", upcoming).Uint64("slot", currentSlot).
			Msg("committee snapshot anchor: could not freeze upcoming epoch's snapshot yet, will retry")
		return
	}

	frozenSnapshotHashMu.Lock()
	defer frozenSnapshotHashMu.Unlock()
	if _, already := frozenSnapshotHash[upcoming]; already {
		return // lost a race with a concurrent caller; first writer wins
	}
	frozenSnapshotHash[upcoming] = committee.HashSnapshot(snap)
}
