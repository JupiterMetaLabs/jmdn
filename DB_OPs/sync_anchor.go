// MODULE: DB_OPs/sync_anchor
// PURPOSE: The accounts-applied anchor — the single honest watermark for account
//          state (F3, RCA_account_sync.md §6c). Stored IN accountsdb so a restore
//          of accountsdb rolls the anchor back together with the balances it
//          describes (invariant I3).
//
// SEMANTICS: anchor = N means "the account effects of every block ≤ N have been
// applied to accountsdb exactly once". Two writers advance it:
//   - live block processing: contiguously only (block == anchor+1), after the
//     block's atomic marker commit succeeds;
//   - reconciliation: to the verified range end, only after post-sync
//     verification passes with zero failed accounts.
//
// SAFETY DIRECTION: an anchor that is TOO LOW is safe for LIVE-applied blocks —
// reconciliation re-covers the range and per-tx `tx_processed:` markers exclude
// already-applied txs (I2). An anchor that is TOO HIGH permanently skips account
// effects (I1 violation). Every writer here rounds DOWN under uncertainty, and
// every target is capped at the locally verified tip (CapAnchorTarget).
//
// CONCURRENCY: appliedAnchorMu serializes the read-modify-write between the two
// writers (both run in this process). This is NOT optional: an unlocked
// interleaving — live reads 10, recon writes 500, live writes 11 — REGRESSES the
// anchor and re-opens a range containing RECON-applied txs, which carry no
// tx_processed markers and would be double-applied on the next run. (Adopted
// from Doc's fix/f3 after adversarial comparison; the original "lost update is
// benign" claim was wrong for recon-applied ranges.)
//
// DO NOT:
//   - Write this key through BatchRestoreAccounts — it is not an account object
//     and must never pass through LWW/merge machinery.
//   - Advance the anchor from any path that has not proven full application.
//   - Seed or advance past the local tip — that is how the legacy SQLite
//     watermark got poisoned with MaxUint64 (see CapAnchorTarget).

package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gossipnode/config"
)

// AppliedAnchorKey is the accountsdb key holding the anchor (uint64 as JSON).
// The "sync:" prefix keeps it outside the address:/did: namespaces that
// BatchRestoreAccounts routes through account-merge logic.
const AppliedAnchorKey = "sync:accounts_last_applied_block"

// appliedAnchorMu serializes all anchor read-modify-write cycles — see the
// CONCURRENCY section of the module header for why this is load-bearing.
var appliedAnchorMu sync.Mutex

// ─── Pure decision rules (unit-tested in sync_anchor_test.go) ────────────────

// NextLiveAnchor is the live path's advancement rule: strictly contiguous.
// Returns (newAnchor, true) only when block is exactly anchor+1. A gap means
// blocks below are missing (downtime) — advancing would skip their effects
// (I1 violation); reconciliation owns gap filling.
func NextLiveAnchor(current, block uint64) (uint64, bool) {
	if block == current+1 {
		return block, true
	}
	return current, false
}

// NextReconAnchor is reconciliation's advancement rule: monotonic max.
// Never moves the anchor backwards.
func NextReconAnchor(current, target uint64) (uint64, bool) {
	if target > current {
		return target, true
	}
	return current, false
}

// ShouldAdvanceReconAnchor gates reconciliation's anchor writes. ALL must hold:
// no reconciliation error, zero failed accounts, and the post-sync verification
// (buildDataMissingTag empty) passed. Anything less and the range is not proven
// applied — advancing would be the exact dishonesty F3 removes (H2/H3).
func ShouldAdvanceReconAnchor(reconErr error, failedAccounts int, verifyPassed bool) bool {
	return reconErr == nil && failedAccounts == 0 && verifyPassed
}

// CapAnchorTarget bounds any anchor target (advance OR legacy seed) at the
// locally verified tip. Two real-world poison sources this neutralizes:
//   - HandleSync substitutes math.MaxUint64 when a legacy peer reports
//     BlockHeight 0 — the pre-F3 SQLite code persisted it, permanently
//     disabling reconciliation;
//   - nodes carrying that poisoned SQLite value would otherwise import it
//     into the new anchor via the migration seed.
func CapAnchorTarget(target, tip uint64) uint64 {
	if target > tip {
		return tip
	}
	return target
}

// ─── Connection plumbing ──────────────────────────────────────────────────────

// withAccountsConn returns a usable accounts-selected connection, acquiring a
// pooled one when conn is nil, plus a release func the caller MUST invoke.
// Uses the explicit Get/Put pair — NOT GetAccountConnectionandPutBack, whose
// auto-return goroutine can recycle the connection mid-use at the ctx deadline.
func withAccountsConn(ctx context.Context, conn *config.PooledConnection) (*config.PooledConnection, func(), error) {
	release := func() {}
	if conn == nil || conn.Client == nil {
		acquired, err := GetAccountsConnections(ctx)
		if err != nil {
			return nil, release, fmt.Errorf("get accounts connection: %w", err)
		}
		conn = acquired
		release = func() { PutAccountsConnection(acquired) }
	}
	if err := ensureAccountsDBSelected(conn); err != nil {
		release()
		return nil, func() {}, fmt.Errorf("select accountsdb: %w", err)
	}
	return conn, release, nil
}

// ─── Storage (accountsdb) ─────────────────────────────────────────────────────

// GetAppliedAnchor reads the anchor from accountsdb. Returns (0, false, nil)
// when the key does not exist yet (fresh node or pre-F3 database).
// conn may be nil — a pooled accounts connection is acquired and returned.
func GetAppliedAnchor(conn *config.PooledConnection) (uint64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, conn)
	if err != nil {
		return 0, false, fmt.Errorf("applied anchor: %w", err)
	}
	defer release()

	return readAnchorLocked(ctx, conn)
}

// readAnchorLocked reads the anchor on an already-selected accounts connection.
// (Named for its usage from within the mutex; safe unlocked for plain reads.)
func readAnchorLocked(ctx context.Context, conn *config.PooledConnection) (uint64, bool, error) {
	entry, err := conn.Client.Client.Get(ctx, []byte(AppliedAnchorKey))
	if err != nil {
		if isNotFoundError(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("applied anchor: read: %w", err)
	}
	var anchor uint64
	if err := json.Unmarshal(entry.Value, &anchor); err != nil {
		return 0, false, fmt.Errorf("applied anchor: parse %q: %w", string(entry.Value), err)
	}
	return anchor, true, nil
}

// writeAnchorLocked writes the anchor on an already-selected accounts
// connection. Callers must hold appliedAnchorMu and have decided the value via
// the pure rules above.
func writeAnchorLocked(conn *config.PooledConnection, value uint64) error {
	if err := SafeCreate(conn.Client, AppliedAnchorKey, value); err != nil {
		return fmt.Errorf("applied anchor: write %d: %w", value, err)
	}
	return nil
}

// AdvanceAppliedAnchorContiguous applies the live path's rule: advance to block
// iff block == anchor+1. Returns the anchor after the call and whether it moved.
// Never returns an error that should fail block processing — the anchor lagging
// is safe (see SAFETY DIRECTION above); callers log and continue.
func AdvanceAppliedAnchorContiguous(conn *config.PooledConnection, block uint64) (uint64, bool, error) {
	return advanceAnchor(conn, block, true)
}

// AdvanceAppliedAnchorTo applies reconciliation's rule: monotonic max to target.
// Returns the anchor after the call and whether it moved.
func AdvanceAppliedAnchorTo(conn *config.PooledConnection, target uint64) (uint64, bool, error) {
	return advanceAnchor(conn, target, false)
}

// advanceAnchor is the single locked read-decide-write cycle for both writers.
// The connection is acquired ONCE and used for both the read and the write —
// acquiring per-step (the original implementation) released the pooled
// connection between read and write and passed nil into the write path.
func advanceAnchor(conn *config.PooledConnection, target uint64, contiguous bool) (uint64, bool, error) {
	appliedAnchorMu.Lock()
	defer appliedAnchorMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, conn)
	if err != nil {
		return 0, false, fmt.Errorf("applied anchor: %w", err)
	}
	defer release()

	current, _, err := readAnchorLocked(ctx, conn)
	if err != nil {
		return 0, false, err
	}

	var next uint64
	var advance bool
	if contiguous {
		next, advance = NextLiveAnchor(current, target)
	} else {
		next, advance = NextReconAnchor(current, target)
	}
	if !advance {
		return current, false, nil
	}
	if err := writeAnchorLocked(conn, next); err != nil {
		return current, false, err
	}
	return next, true, nil
}

// SeedAppliedAnchor writes the anchor ONLY if the key does not exist yet.
// Migration path: pre-F3 nodes tracked reconciliation in SQLite
// (fastsync:last_reconciled_block); reconciliation-applied txs carry NO
// tx_processed markers, so re-reconciling that range would double-apply.
// Seeding from the legacy value trades a bounded, repairable risk (the legacy
// value may be dishonestly HIGH → some effects stay missing until the repair
// job runs) against guaranteed corruption (systematic double-apply).
//
// CALLERS MUST CAP legacy via CapAnchorTarget(legacy, localTip) first — the
// legacy SQLite value can carry the MaxUint64 poison. Returns whether a seed
// write happened.
func SeedAppliedAnchor(conn *config.PooledConnection, legacy uint64) (bool, error) {
	if legacy == 0 {
		return false, nil
	}

	appliedAnchorMu.Lock()
	defer appliedAnchorMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("applied anchor seed: %w", err)
	}
	defer release()

	_, found, err := readAnchorLocked(ctx, conn)
	if err != nil {
		return false, err
	}
	if found {
		return false, nil
	}
	if err := writeAnchorLocked(conn, legacy); err != nil {
		return false, err
	}
	return true, nil
}

// ─── Processed-tx marker filter (dual-DB) ─────────────────────────────────────

// FilterProcessedTxMarkers returns the subset of txHashes whose `tx_processed:`
// marker says APPLIED, dual-reading both databases with value-aware semantics
// (F4: value -1 = revoked by rollback = NOT processed; see DB_OPs/tx_markers.go).
//
// DB precedence: accountsdb is authoritative when the key exists there — F4
// writes markers AND rollback revocations to accountsdb, and a -1 revocation
// must never be overridden by a stale legacy marker. defaultdb is consulted
// only for keys absent from accountsdb (legacy populations: current-era
// defaultdb markers + pre-F4 history; RCA §6b, empirical 2026-07-09).
//
// Reconciliation uses this filter to exclude already-live-applied txs from
// delta computation (I2); missing either DB re-applies those txs.
//
// Errors are returned, never swallowed: a failed filter must abort delta
// computation (fail closed) — proceeding without exclusions double-applies.
//
// Time: O(len(txHashes)) split into one GetAll round-trip per DB per 1000 keys.
func FilterProcessedTxMarkers(txHashes []string) (map[string]bool, error) {
	processed := make(map[string]bool, len(txHashes))
	if len(txHashes) == 0 {
		return processed, nil
	}

	keys := make([][]byte, 0, len(txHashes))
	keyToHash := make(map[string]string, len(txHashes))
	for _, h := range txHashes {
		k := TxProcessedKey(h)
		keys = append(keys, []byte(k))
		keyToHash[k] = h
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// accountsdb FIRST — authoritative for every key present there.
	acctValues := make(map[string][]byte, len(txHashes))
	acctConn, err := GetAccountsConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("marker filter: accounts connection: %w", err)
	}
	acctErr := func() error {
		defer PutAccountsConnection(acctConn)
		if err := ensureAccountsDBSelected(acctConn); err != nil {
			return fmt.Errorf("select accountsdb: %w", err)
		}
		return collectMarkerValues(ctx, acctConn, keys, keyToHash, acctValues)
	}()
	if acctErr != nil {
		return nil, fmt.Errorf("marker filter (accountsdb): %w", acctErr)
	}
	for h, raw := range acctValues {
		if markerValueApplied(raw) {
			processed[h] = true
		}
		// present-but-revoked: decided; defaultdb must not override.
	}

	// defaultdb (legacy) — only keys accountsdb knows nothing about.
	// Explicit Get/Put — NOT the auto-return helper, whose recycle-at-deadline
	// goroutine races long GetAll sequences.
	legacyKeys := make([][]byte, 0, len(keys))
	for _, k := range keys {
		if _, decided := acctValues[keyToHash[string(k)]]; !decided {
			legacyKeys = append(legacyKeys, k)
		}
	}
	if len(legacyKeys) > 0 {
		mainValues := make(map[string][]byte, len(legacyKeys))
		mainConn, err := GetMainDBConnection(ctx)
		if err != nil {
			return nil, fmt.Errorf("marker filter: main connection: %w", err)
		}
		mainErr := func() error {
			defer PutMainDBConnection(mainConn)
			if err := ensureMainDBSelected(mainConn); err != nil {
				return fmt.Errorf("select defaultdb: %w", err)
			}
			return collectMarkerValues(ctx, mainConn, legacyKeys, keyToHash, mainValues)
		}()
		if mainErr != nil {
			return nil, fmt.Errorf("marker filter (defaultdb): %w", mainErr)
		}
		for h, raw := range mainValues {
			if markerValueApplied(raw) {
				processed[h] = true
			}
		}
	}

	return processed, nil
}

// collectMarkerValues GetAlls keys in chunks on the given (already-selected)
// connection and records found raw values into out (keyed by tx hash). immudb
// GetAll tolerates missing keys per-key (verified against immudb v1.10.0), so
// absence is not an error.
func collectMarkerValues(ctx context.Context, conn *config.PooledConnection, keys [][]byte, keyToHash map[string]string, out map[string][]byte) error {
	const chunk = 1000
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		entries, err := conn.Client.Client.GetAll(ctx, keys[i:end])
		if err != nil {
			return fmt.Errorf("GetAll [%d:%d]: %w", i, end, err)
		}
		if entries == nil {
			continue
		}
		for _, e := range entries.Entries {
			if e == nil {
				continue
			}
			if h, ok := keyToHash[string(e.Key)]; ok {
				out[h] = e.Value
			}
		}
	}
	return nil
}

// TransactionOnMainDB runs fn as one atomic ExecAll explicitly on defaultdb.
// Exists because Transaction() inherits whatever database the session last
// selected — marker commits previously landed in defaultdb or accountsdb
// depending on incidental call order (RCA §6b, H0). Every marker commit MUST
// use this instead of Transaction directly.
func TransactionOnMainDB(conn *config.PooledConnection, fn func(tx *config.ImmuTransaction) error) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("TransactionOnMainDB: nil connection")
	}
	if err := ensureMainDBSelected(conn); err != nil {
		return fmt.Errorf("TransactionOnMainDB: select defaultdb: %w", err)
	}
	return Transaction(conn.Client, fn)
}
