package messaging

// Durable-side companion to the in-memory committee.BeaconSource.
//
// BeaconSource is a map behind a mutex with a 3-epoch retention window and no
// persistence of any kind. That is fine for a process that never stops and
// fatal for one that does: the mix that produced an epoch's entropy is gone
// once the epoch closes (see entropy_mix_store.go's retention, and the fold
// state it derives from), so a restarted node cannot re-seal the epoch and
// cannot verify a peer's proof for it either. Persisting the 32-byte output is
// the only recovery that works.
//
// This file is the messaging-side glue: it reads what the beacon already holds
// and hands it to DB_OPs. It never invents a value and never writes anything
// the beacon has not already accepted — BeaconSource.Publish is idempotent for
// an identical value and refuses a conflicting one, so everything reachable
// here was either sealed locally or verified through Pipeline.Accept.

import (
	"errors"
	"fmt"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
)

// ErrNoEntropyToPersist reports that the beacon does not hold this epoch, so
// there was nothing verified to write.
//
// It exists because "nothing to persist" means opposite things at the two call
// sites, and returning nil for both hid a real failure at one of them:
//
//   - entropy_vdf_accept.go adopts a peer's proof and is routinely called for
//     epochs this node never published. Absent is expected there; that caller
//     discards the return entirely.
//   - Sequencer/vdf_sealer.go calls this ONLY inside `if err == nil` after a
//     successful Seal, and Seal publishes the entropy into the sink as a side
//     effect. So by construction the epoch MUST be in the beacon by then. If it
//     is not, the publish silently did not land — and before this sentinel that
//     surfaced as a nil return, i.e. a seal reporting success having written
//     nothing.
//
// Callers that can legitimately ask about an epoch they do not hold should use
// errors.Is on this and ignore it. Callers that can only reach this function
// for an epoch they just published should treat it as a failure.
//
// NOTE ON THE MECHANISM, because the obvious reading is wrong:
// committee.BeaconSource.EpochEntropy is a plain map lookup under an RLock —
// activeBeacon() returns that concrete type, not an interface — so it performs
// no I/O and has exactly one error path, ErrEntropyUnavailable ("this epoch is
// not in the map"). There is no read-failure case to distinguish here; the
// distinction that matters is which caller is asking.
var ErrNoEntropyToPersist = errors.New("messaging: epoch not published locally — nothing verified to persist")

// PersistEpochEntropy durably records the entropy this node holds for epoch.
//
// No-op when no beacon is installed (Stage 1). Returns ErrNoEntropyToPersist
// when a beacon exists but does not hold this epoch — see that sentinel for
// why the two are not both nil.
//
// A KV write failure is logged and returned but is NOT fatal to the caller by
// itself: losing durability for one epoch degrades restart recovery, while
// failing the block path over a KV write would turn a storage hiccup into a
// consensus stall. It is the caller's choice whether to escalate.
func PersistEpochEntropy(epoch uint64) error {
	beacon := activeBeacon()
	if beacon == nil {
		return nil
	}
	entropy, err := beacon.EpochEntropy(committee.EntropyEpoch(epoch))
	if err != nil {
		return fmt.Errorf("%w (epoch %d): %w", ErrNoEntropyToPersist, epoch, err)
	}
	if err := DB_OPs.RecordBeaconEntropy(nil, epoch, entropy); err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).
			Msg("entropy: failed to persist finalised epoch entropy — this epoch will NOT survive a " +
				"restart, and it cannot be recomputed once its mix ages out. Investigate the KV write")
		return err
	}
	log.Debug().Uint64("epoch", epoch).Msg("entropy: finalised epoch entropy persisted")
	return nil
}

// RehydrateBeaconFromDisk republishes persisted epoch entropy into a
// freshly-constructed BeaconSource. Call once at startup, after the sink is
// created and BEFORE the consensus loop starts.
//
// Replay order does NOT affect the restored set. BeaconSource.evictLocked uses
// cutoff = newest-retain with newest = max(all published), so what survives is
// {e : e >= max-retain} regardless of arrival order — verified by
// TestRehydrationSurvivorSetIsOrderIndependent, which exists because an earlier
// version of this comment asserted the opposite.
//
// # What is and is not fatal
//
// Only a CONFLICTING durable value is fatal. Publish refuses it, and a record
// that disagrees with what this process already holds means the durable state
// is corrupt or belongs to another network — seating committees from it would
// be worse than not starting Stage 2.
//
// Everything else is a soft miss: no records yet (a fresh node, or one that
// predates this feature), or a read error on one epoch. Those are logged and
// skipped. An earlier version treated ANY error as fatal, which turned the
// absence of a store into a refusal to install the beacon at all — caught by
// Sequencer's own beacon-install tests.
func RehydrateBeaconFromDisk(sink *committee.BeaconSource) (restored int, err error) {
	if sink == nil {
		return 0, nil
	}
	epochs, found, lerr := DB_OPs.BeaconEntropyEpochsToRestore(nil, committee.MinRetainedEpochs+1)
	if lerr != nil {
		log.Warn().Err(lerr).
			Msg("entropy: could not enumerate persisted beacon entropy — starting with an empty " +
				"beacon. Epochs finalised before this restart cannot be seated until they are " +
				"re-published or adopted from a peer's proof")
		return 0, nil
	}
	if !found {
		return 0, nil
	}

	for _, e := range epochs {
		entropy, ok, gerr := DB_OPs.GetBeaconEntropy(nil, e)
		if gerr != nil {
			log.Warn().Err(gerr).Uint64("epoch", e).
				Msg("entropy: skipping unreadable persisted epoch during rehydration")
			continue
		}
		if !ok || len(entropy) == 0 {
			continue
		}
		if perr := sink.Publish(e, entropy); perr != nil {
			// Conflict — fail closed. See the doc comment.
			return restored, fmt.Errorf("entropy: persisted value for epoch %d conflicts with the "+
				"live beacon: %w", e, perr)
		}
		restored++
	}

	if restored > 0 {
		log.Info().Int("epochs_restored", restored).
			Msg("entropy: rehydrated persisted beacon entropy — this node can seat committees for " +
				"epochs it finalised before the restart, without re-evaluating any VDF")
	}
	return restored, nil
}
