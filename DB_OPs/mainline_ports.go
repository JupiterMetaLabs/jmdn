// MODULE: DB_OPs/mainline_ports.go
// PURPOSE: Thebe-backed ports of main-line (AVC v2 era) package-level helpers
// whose original ImmuDB implementations lived in the deleted
// account_immuclient.go. Semantics mirror main @ cfb4eef; storage goes through
// the pooled ThebeHandle (getHandle) / compat helpers instead of ImmuDB.
//
// PORTED HERE:
//   - NormalizePropagatedAccountState — pure, copied verbatim (cf09a26).
//   - ListAccountsPaginatedFrom — keyset-cursor contract preserved; the cursor
//     is the last returned address over ListAccountsAfterCtx (SQL keyset on
//     LOWER(address), canonical fingerprint order), not an ImmuDB SeekKey scan.
//   - CountAccountsWithTimeout — real count via CountAccountsCtx (the compat
//     CountBuilder stubs return 0 and must not be used for the stats seed).
//
// DO NOT:
//   - Add new ImmuDB-flavored helpers here. New code uses getHandle() directly.

package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
)

// NormalizePropagatedAccountState resets the volatile ledger fields of an
// account received via DID propagation to their canonical initial values.
// Balance, TxNonce, and TxCountSent are owned by block processing and
// reconciliation, so an identity-propagation event always initializes them to
// zero. This is the single source of truth for that policy, shared by the store
// path (StorePropagatedAccount) and the forward path (HandleDIDStream) so both
// the stored and the re-broadcast copy stay consistent.
//
// Left untouched: the ART identity Nonce (preserved for Fastsync ART routing)
// and CreatedAt/UpdatedAt (timestamp policy is owned by the caller — the store
// path stamps them locally; the forward path keeps them so downstream LWW
// ordering is not affected). Pure and unit-tested.
//
// Returns true if any reset field carried a non-canonical value on input, so
// callers can record it for observability.
func NormalizePropagatedAccountState(acc *Account) bool {
	if acc == nil {
		return false
	}
	adjusted := (acc.Balance != "" && acc.Balance != "0") ||
		acc.TxNonce != 0 ||
		acc.TxCountSent != 0
	acc.Balance = "0"
	acc.TxNonce = 0
	acc.TxCountSent = 0
	return adjusted
}

// ListAccountsPaginatedFrom retrieves up to limit accounts starting after the
// opaque cursor seekKey in the backend listing order. seekKey=nil starts from
// the beginning. Returns the accounts and the next cursor; pass it as seekKey
// on the next call to continue. An empty result with a nil cursor means the
// listing is exhausted.
//
// Order: canonical ascending LOWER(address) (node-independent, matching
// consensushash.normAddr — the P2.5 fingerprint folds accounts in exactly this
// order). The cursor is the last returned address (keyset), so each page is an
// index range scan (idx_accounts_address_lower) instead of an OFFSET that
// re-sorted the whole table: the fingerprint scan, which runs on every applied
// block, went from O(N^2/page) to O(N).
func ListAccountsPaginatedFrom(conn *config.PooledConnection, limit int, seekKey []byte, _ string) ([]*Account, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	accs, err := ListAccountsAfterCtx(ctx, conn, string(seekKey), limit)
	if err != nil {
		return nil, nil, fmt.Errorf("ListAccountsPaginatedFrom: %w", err)
	}
	if len(accs) == 0 {
		return nil, nil, nil
	}
	return accs, []byte(accs[len(accs)-1].Address.Hex()), nil
}

// CountAccountsWithTimeout returns the total number of accounts, bounded by
// countTimeout. Used by the one-time explorer stats seed in main.go — off the
// request path, so a long deadline is fine.
func CountAccountsWithTimeout(countTimeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), countTimeout)
	defer cancel()
	n, err := CountAccountsCtx(ctx)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
