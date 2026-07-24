// MODULE: DB_OPs/equivocation
// PURPOSE: Durable per-height "first-seen block hash" marker backing the
//          consensus equivocation detector (P6). Persisting the record lets a
//          node reject a conflicting block at a height it first saw BEFORE a
//          process restart — the in-memory detector alone loses that record on
//          restart, reopening a pre-commit double-sign window.
//
// SAFETY DIRECTION: only FULLY VALIDATED blocks are recorded (the caller,
// messaging.checkEquivocation, runs last in validateRemoteBlock), so an
// attacker cannot poison this map with unvalidated (height, hash) pairs.
//
// STORAGE: accountsdb, mirroring sync_anchor's small-marker pattern (same DB
// selection plumbing, same not-found handling). Key format is frozen.

package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/config"
)

// EquivocationHeightKey builds the durable first-seen-hash key for a height.
// Frozen format — must stay stable so records survive upgrades.
func EquivocationHeightKey(height uint64) string {
	return fmt.Sprintf("equivocation_height:%d", height)
}

// GetEquivocationHash returns the first-seen block-hash hex recorded for height.
// found=false (nil error) when nothing is stored yet. conn may be nil — a
// pooled accounts connection is acquired and released internally.
func GetEquivocationHash(conn *config.PooledConnection, height uint64) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, conn)
	if err != nil {
		return "", false, fmt.Errorf("equivocation read: %w", err)
	}
	defer release()

	entry, err := conn.Client.Client.Get(ctx, []byte(EquivocationHeightKey(height)))
	if err != nil {
		if isNotFoundError(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("equivocation read height %d: %w", height, err)
	}
	var hashHex string
	if err := json.Unmarshal(entry.Value, &hashHex); err != nil {
		return "", false, fmt.Errorf("equivocation parse height %d (%q): %w", height, string(entry.Value), err)
	}
	return hashHex, true, nil
}

// RecordEquivocationHash durably stores hashHex as the first-seen block hash for
// height. Callers record only after a block fully validates. conn may be nil.
func RecordEquivocationHash(conn *config.PooledConnection, height uint64, hashHex string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, conn)
	if err != nil {
		return fmt.Errorf("equivocation write: %w", err)
	}
	defer release()

	if err := SafeCreate(conn.Client, EquivocationHeightKey(height), hashHex); err != nil {
		return fmt.Errorf("equivocation write height %d: %w", height, err)
	}
	return nil
}
