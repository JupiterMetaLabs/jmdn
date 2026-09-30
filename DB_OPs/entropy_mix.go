package DB_OPs

// Durable copy of each finalised entropy mix, mix(e) - the VDF input for
// ENTROPY-(e+1).
//
// mix(e) is a pure function of committed chain data (the randao_reveals of
// epoch e's window blocks, or the aggregate-signature fallback over committed
// prev_agg_cert), so this record is a recovery copy, never a source of truth:
// it lets a restarted node verify the next epoch's VDF proof (and restart its
// own sealer) without first replaying the window. messaging re-derives the
// value by replay at startup and treats any disagreement as an alarm.
//
// Stored in the same durable sync KV as the beacon entropy and the
// latest_block marker. First writer wins: a second, different value for the
// same epoch is refused, never overwritten.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/config"
)

const entropyMixPrefix = "entropy_mix:"

// EntropyMixKey is the KV key for epoch e's mix.
func EntropyMixKey(epoch uint64) string { return fmt.Sprintf("%s%d", entropyMixPrefix, epoch) }

// EntropyMixRecord is the persisted form of one finalised mix.
type EntropyMixRecord struct {
	Epoch   uint64 `json:"epoch"`
	SeedHex string `json:"seed"`    // 32-byte mix, lowercase hex
	Outcome string `json:"outcome"` // "mixed" or "fallback"
}

// GetEntropyMix returns epoch's persisted mix record, if any.
func GetEntropyMix(conn *config.PooledConnection, epoch uint64) (EntropyMixRecord, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return EntropyMixRecord{}, false, fmt.Errorf("entropy mix read: %w", err)
	}
	raw, err := h.GetSyncKV(EntropyMixKey(epoch))
	if err != nil {
		if isNotFoundError(err) {
			return EntropyMixRecord{}, false, nil
		}
		return EntropyMixRecord{}, false, fmt.Errorf("entropy mix read epoch %d: %w", epoch, err)
	}
	if raw == nil {
		return EntropyMixRecord{}, false, nil
	}
	var rec EntropyMixRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return EntropyMixRecord{}, false, fmt.Errorf("entropy mix parse epoch %d: %w", epoch, err)
	}
	if rec.Epoch != epoch {
		return EntropyMixRecord{}, false, fmt.Errorf("entropy mix epoch %d: record claims epoch %d", epoch, rec.Epoch)
	}
	if b, err := hex.DecodeString(rec.SeedHex); err != nil || len(b) != 32 {
		return EntropyMixRecord{}, false, fmt.Errorf("entropy mix epoch %d: malformed seed", epoch)
	}
	return rec, true, nil
}

// RecordEntropyMix stores epoch's mix. Idempotent for an identical value; a
// DIFFERENT value for an epoch that already has one is refused.
func RecordEntropyMix(conn *config.PooledConnection, rec EntropyMixRecord) error {
	if b, err := hex.DecodeString(rec.SeedHex); err != nil || len(b) != 32 {
		return fmt.Errorf("entropy mix write epoch %d: seed must be 32 bytes of hex", rec.Epoch)
	}
	rec.SeedHex = strings.ToLower(rec.SeedHex)
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("entropy mix write: %w", err)
	}
	key := EntropyMixKey(rec.Epoch)
	existing, gerr := h.GetSyncKV(key)
	if gerr != nil && !isNotFoundError(gerr) {
		return fmt.Errorf("entropy mix pre-read (fail closed) epoch %d: %w", rec.Epoch, gerr)
	}
	if existing != nil {
		var prev EntropyMixRecord
		if uerr := json.Unmarshal(existing, &prev); uerr != nil {
			return fmt.Errorf("entropy mix parse existing epoch %d: %w", rec.Epoch, uerr)
		}
		if strings.EqualFold(prev.SeedHex, rec.SeedHex) {
			return nil
		}
		return fmt.Errorf("entropy mix epoch %d already has a DIFFERENT value (stored %s, got %s); refusing to overwrite",
			rec.Epoch, prev.SeedHex, rec.SeedHex)
	}
	val, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("entropy mix encode epoch %d: %w", rec.Epoch, err)
	}
	if err := h.PutSyncKV(key, val); err != nil {
		return fmt.Errorf("entropy mix write epoch %d: %w", rec.Epoch, err)
	}
	return nil
}
