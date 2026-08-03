// MODULE: DB_OPs/equivocation
// PURPOSE: Durable per-height "first-seen block hash" marker backing the
//          consensus equivocation detector. Persisting the record lets a
//          node reject a conflicting block at a height it first saw BEFORE a
//          process restart — the in-memory detector alone loses that record on
//          restart, so the durable marker keeps equivocation detection working
//          across restarts.
//
// SAFETY DIRECTION: only FULLY VALIDATED blocks are recorded (the caller,
// messaging.checkEquivocation, runs last in validateRemoteBlock), so only
// fully-validated (height, hash) pairs are recorded.
//
// STORAGE: ThebeDB sync-state KV via the pooled handle (same population as the
// tx_processed markers — see tx_markers.go). Key format is frozen; the value
// stays JSON-encoded (string) to match the pre-migration on-disk records.

package DB_OPs

import (
	"encoding/json"
	"fmt"

	"gossipnode/config"
)

// EquivocationHeightKey builds the durable first-seen-hash key for a height.
// Frozen format — must stay stable so records survive upgrades.
func EquivocationHeightKey(height uint64) string {
	return fmt.Sprintf("equivocation_height:%d", height)
}

// GetEquivocationHash returns the first-seen block-hash hex recorded for height.
// found=false (nil error) when nothing is stored yet. conn may be nil — the
// process-wide Thebe handle is used.
func GetEquivocationHash(conn *config.PooledConnection, height uint64) (string, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return "", false, fmt.Errorf("equivocation read: %w", err)
	}
	raw, err := h.GetSyncKV(EquivocationHeightKey(height))
	if err != nil {
		if isNotFoundError(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("equivocation read height %d: %w", height, err)
	}
	if raw == nil {
		return "", false, nil
	}
	var hashHex string
	if err := json.Unmarshal(raw, &hashHex); err != nil {
		return "", false, fmt.Errorf("equivocation parse height %d (%q): %w", height, string(raw), err)
	}
	return hashHex, true, nil
}

// RecordEquivocationHash durably stores hashHex as the first-seen block hash for
// height. Callers record only after a block fully validates. conn may be nil.
//
// FIRST-SEEN GUARD: an existing record is never overwritten (the ImmuDB
// implementation used a create-only write for the same reason). A conflicting
// hash at a recorded height is exactly the equivocation the detector reports —
// keeping the original record is what makes that detection stable across
// restarts.
func RecordEquivocationHash(conn *config.PooledConnection, height uint64, hashHex string) error {
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("equivocation write: %w", err)
	}
	key := EquivocationHeightKey(height)
	if raw, gerr := h.GetSyncKV(key); gerr == nil && raw != nil {
		return nil // first-seen record already present — never overwrite
	}
	val, err := json.Marshal(hashHex)
	if err != nil {
		return fmt.Errorf("equivocation encode height %d: %w", height, err)
	}
	if err := h.PutSyncKV(key, val); err != nil {
		return fmt.Errorf("equivocation write height %d: %w", height, err)
	}
	return nil
}
