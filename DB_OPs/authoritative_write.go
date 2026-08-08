// MODULE: DB_OPs/authoritative_write.go
// PURPOSE: Raw (merge-bypassing) account batch write for the TWO AUTHORITATIVE
// balance writers — the live executor (ApplyTxAtomic) and reconciliation
// (commitReconGroup). Both run under LockStateApply and compute ABSOLUTE
// account documents from the stored base, so their writes must win
// unconditionally — the pre-Thebe ExecAll semantics that
// state_apply_lock.go documents.
//
// WHY THIS EXISTS (KB-review findings 1-2, 2026-08-06): routing these writers
// through BatchRestoreAccounts put them behind mergeAccountForWrite's
// last-writer-wins gate. storeAccount stamps every stored doc with wall-clock
// UpdatedAt, while authoritative docs carry the BLOCK timestamp — so any
// account written after block T beat a reconciliation doc for block T, the
// delta was silently dropped, and the tx_processed marker was still written:
// permanently lost credits, invisible to the sync monitor.
//
// DO NOT:
//   - Use this from sync-path writers (account-sync restore, DID propagation,
//     updates payloads). Those are UNCOORDINATED and MUST stay behind the
//     BatchRestoreAccounts merge gate (LWW + zero-balance clobber guard).
//   - Call without holding LockStateApply.

package DB_OPs

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// BatchPutAccountsAuthoritative writes absolute account documents WITHOUT the
// LWW merge gate. Entries use the same shape as BatchRestoreAccounts so the
// two authoritative call sites are drop-in. Caller MUST hold LockStateApply.
func BatchPutAccountsAuthoritative(entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}
	for i, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("invalid entry (empty key or nil value)")
		}
		var doc Account
		if err := json.Unmarshal(e.Value, &doc); err != nil {
			return fmt.Errorf("BatchPutAccountsAuthoritative[%d] key=%s: unmarshal: %w", i, e.Key, err)
		}
		if doc.Address == (common.Address{}) {
			continue // skip malformed entries (matches BatchRestoreAccounts)
		}
		if err := storeAccount(nil, &doc); err != nil {
			return fmt.Errorf("BatchPutAccountsAuthoritative[%d] key=%s: store: %w", i, e.Key, err)
		}
	}
	return nil
}
