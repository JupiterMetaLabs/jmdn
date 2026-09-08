// MODULE: DB_OPs/beacon_entropy
// PURPOSE: Durable per-epoch Stage-2 beacon entropy, so a node that restarts
//          does not lose entropy it can NEVER recompute.
//
// WHY RE-COMPUTATION IS NOT AN OPTION. Both recovery primitives in avc/beacon
// take the RANDAO mix as an argument — Pipeline.SealLocally(forEpoch, mix) and
// Pipeline.Accept(forEpoch, mix, proof). The mix lives in process memory
// (messaging's accumulator and fallback fold) and the epoch that produced it
// has closed by the time a restarted node comes back. So a restarted node has
// no mix, cannot re-seal, and cannot verify anybody's proof for that epoch.
// The only recovery that works is persisting the 32-byte OUTPUT and replaying
// it — which is what this file exists for.
//
// SAFETY DIRECTION: only entropy this node already accepted into its own
// BeaconSource is written here. committee.BeaconSource.Publish is idempotent
// for an identical value and REFUSES a conflicting one, so anything that
// reached the sink was either sealed locally or verified through
// Pipeline.Accept. Unverified entropy never gets this far.
//
// STORAGE: ThebeDB sync-state KV via the pooled handle, same population and
// same shape as the equivocation markers (see equivocation.go). Key format is
// frozen; the value is JSON-encoded to match every other record in this KV.

package DB_OPs

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"
)

// beaconEntropyPrefix namespaces the per-epoch records. Frozen — records must
// survive upgrades.
const beaconEntropyPrefix = "beacon_entropy:"

// BeaconEntropyKey builds the durable key for an epoch's finalised entropy.
func BeaconEntropyKey(epoch uint64) string {
	return fmt.Sprintf("%s%d", beaconEntropyPrefix, epoch)
}

// GetBeaconEntropy returns the 32-byte entropy recorded for epoch.
// found=false (nil error) when nothing is stored. conn may be nil.
func GetBeaconEntropy(conn *config.PooledConnection, epoch uint64) ([]byte, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return nil, false, fmt.Errorf("beacon entropy read: %w", err)
	}
	raw, err := h.GetSyncKV(BeaconEntropyKey(epoch))
	if err != nil {
		if isNotFoundError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("beacon entropy read epoch %d: %w", epoch, err)
	}
	if raw == nil {
		return nil, false, nil
	}
	var entropyHex string
	if err := json.Unmarshal(raw, &entropyHex); err != nil {
		return nil, false, fmt.Errorf("beacon entropy parse epoch %d (%q): %w", epoch, string(raw), err)
	}
	out, err := hex.DecodeString(entropyHex)
	if err != nil {
		return nil, false, fmt.Errorf("beacon entropy hex epoch %d: %w", epoch, err)
	}
	return out, true, nil
}

// RecordBeaconEntropy durably stores an epoch's finalised entropy.
//
// FINALITY GUARD, mirroring committee.BeaconSource.Publish exactly: an
// identical value is a no-op, and a CONFLICTING value is refused with an
// error rather than overwritten. An epoch's entropy is final — silently
// replacing it would re-seat every committee in that epoch and retroactively
// invalidate certificates that were already correct. A conflict here means
// this node accepted two different values for one epoch, which is a bug worth
// surfacing loudly, not smoothing over.
//
// Fails CLOSED on a read error, for the reason equivocation.go documents: a
// read error that fell through would overwrite the durable record this
// function exists to protect.
func RecordBeaconEntropy(conn *config.PooledConnection, epoch uint64, entropy []byte) error {
	if len(entropy) == 0 {
		return fmt.Errorf("beacon entropy write epoch %d: refusing to store empty entropy", epoch)
	}
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("beacon entropy write: %w", err)
	}
	key := BeaconEntropyKey(epoch)

	raw, gerr := h.GetSyncKV(key)
	if gerr != nil && !isNotFoundError(gerr) {
		return fmt.Errorf("beacon entropy pre-read (fail closed) epoch %d: %w", epoch, gerr)
	}
	if raw != nil {
		var existingHex string
		if uerr := json.Unmarshal(raw, &existingHex); uerr != nil {
			return fmt.Errorf("beacon entropy parse existing epoch %d: %w", epoch, uerr)
		}
		if strings.EqualFold(existingHex, hex.EncodeToString(entropy)) {
			return nil // identical — idempotent
		}
		return fmt.Errorf("beacon entropy epoch %d already has a DIFFERENT value (stored %s, got %s); "+
			"overwriting would re-seat every committee in that epoch",
			epoch, existingHex, hex.EncodeToString(entropy))
	}

	val, err := json.Marshal(hex.EncodeToString(entropy))
	if err != nil {
		return fmt.Errorf("beacon entropy encode epoch %d: %w", epoch, err)
	}
	if err := h.PutSyncKV(key, val); err != nil {
		return fmt.Errorf("beacon entropy write epoch %d: %w", epoch, err)
	}

	// Advance the newest pointer, monotonically. Restore reads this instead of
	// scanning, so a pointer that went backwards would hide newer records. A
	// failure here is non-fatal: the entropy record itself is already durable,
	// and the pointer self-heals on the next write of a higher epoch.
	if newest, found, rerr := readBeaconEntropyNewest(h); rerr == nil && (!found || epoch > newest) {
		if nv, merr := json.Marshal(epoch); merr == nil {
			_ = h.PutSyncKV(beaconEntropyNewestKey, nv)
		}
	}
	return nil
}

// beaconEntropyNewestKey holds the highest epoch ever written, so restore can
// find the records WITHOUT a prefix scan.
//
// There is no prefix scan available: DB_OPs.GetAllKeys is a stub that always
// errors ("ImmuDB removed; use ThebeDB SQL queries instead"). An earlier
// version of this file called it and consequently failed on every startup —
// caught by Sequencer's own beacon-install tests. A single monotonic pointer
// plus a bounded probe needs no scan and no manifest that can drift out of
// sync with the records it describes.
const beaconEntropyNewestKey = "beacon_entropy_newest"

func readBeaconEntropyNewest(h store.ThebeHandle) (uint64, bool, error) {
	raw, err := h.GetSyncKV(beaconEntropyNewestKey)
	if err != nil {
		if isNotFoundError(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if raw == nil {
		return 0, false, nil
	}
	var newest uint64
	if err := json.Unmarshal(raw, &newest); err != nil {
		return 0, false, fmt.Errorf("beacon entropy newest parse (%q): %w", string(raw), err)
	}
	return newest, true, nil
}

// BeaconEntropyEpochsToRestore returns the epochs worth probing at startup:
// the newest persisted epoch and the `window` epochs below it.
//
// Bounded by construction — it never returns more than window+1 entries, so a
// long-running node does not accumulate restore work. Ascending, which is
// cosmetic (see ListBeaconEntropyEpochs) but keeps logs deterministic.
//
// found=false means nothing has ever been persisted: a fresh node, or one that
// predates this feature. That is not an error.
func BeaconEntropyEpochsToRestore(conn *config.PooledConnection, window uint64) ([]uint64, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return nil, false, fmt.Errorf("beacon entropy restore list: %w", err)
	}
	newest, found, err := readBeaconEntropyNewest(h)
	if err != nil {
		return nil, false, fmt.Errorf("beacon entropy restore list: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	lo := uint64(0)
	if newest > window {
		lo = newest - window
	}
	out := make([]uint64, 0, window+1)
	for e := lo; e <= newest; e++ {
		out = append(out, e)
	}
	return out, true, nil
}
