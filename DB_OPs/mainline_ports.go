// MODULE: DB_OPs/mainline_ports.go
// PURPOSE: Thebe-backed ports of main-line (AVC v2 era) package-level helpers
// whose original ImmuDB implementations lived in the deleted
// account_immuclient.go. Semantics mirror main @ cfb4eef; storage goes through
// the pooled ThebeHandle (getHandle) / compat helpers instead of ImmuDB.
//
// PORTED HERE:
//   - NormalizePropagatedAccountState — pure, copied verbatim (cf09a26).
//   - ListAccountsPaginatedFrom — keyset-cursor contract preserved; cursor is
//     now an opaque decimal offset over ListAccountsPaginatedCtx (SQL
//     `ORDER BY created_at ASC`), not an ImmuDB SeekKey scan. See
//     docs/RECONCILE-thebe-sc.md for the ordering caveat.
//   - CountAccountsWithTimeout — real count via CountAccountsCtx (the compat
//     CountBuilder stubs return 0 and must not be used for the stats seed).
//
// DO NOT:
//   - Add new ImmuDB-flavored helpers here. New code uses getHandle() directly.

package DB_OPs

import (
	"context"
	"fmt"
	"strconv"
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
// Port note: the ImmuDB implementation scanned ascending by KEY with a SeekKey
// cursor. This port pages the ThebeDB SQL listing (now ORDER BY LOWER(address)
// ASC — node-independent, matching consensushash.normAddr, so the P2.5 fingerprint
// is identical across nodes regardless of insertion history) with the cursor
// carrying an opaque numeric offset. Offset pagination is safe here because the
// fingerprint scan runs under the apply lock (stable snapshot). Follow-up: an
// address keyset cursor would drop the O(N·pages) offset cost for large N.
func ListAccountsPaginatedFrom(_ *config.PooledConnection, limit int, seekKey []byte, _ string) ([]*Account, []byte, error) {
	offset := 0
	if len(seekKey) > 0 {
		n, err := strconv.Atoi(string(seekKey))
		if err != nil {
			return nil, nil, fmt.Errorf("ListAccountsPaginatedFrom: bad cursor %q: %w", string(seekKey), err)
		}
		offset = n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	accs, err := ListAccountsPaginatedCtx(ctx, limit, offset)
	if err != nil {
		return nil, nil, err
	}
	if len(accs) == 0 {
		return nil, nil, nil
	}
	return accs, []byte(strconv.Itoa(offset + len(accs))), nil
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
