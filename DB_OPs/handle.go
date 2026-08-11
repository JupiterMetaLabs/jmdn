// MODULE: DB_OPs/handle.go
// PURPOSE: Provide the process-wide store.ThebeHandle singleton and the
//          getHandle() accessor used by all DB_OPs shim functions.
//          Also provides account type conversion helpers shared between
//          the new ThebeDB path and BulkGetAccounts.go.
//
// PATTERN:
//   - SetGlobalHandle is called once from main() after backend.New() is constructed.
//   - getHandle(conn) first tries to cast conn.Client to store.ThebeHandle (for
//     test injection or future pooled handles), then falls back to globalThebeHandle.
//
// DO NOT:
//   - Call SetGlobalHandle more than once in production.
//   - Store per-request state here — this is a stateless accessor.

package DB_OPs

import (
	"fmt"
	"sync"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// globalThebeHandle is the process-wide ThebeHandle set once at startup.
// It is used as a fallback when conn.Client does not satisfy store.ThebeHandle
// (e.g. legacy callers that pass a nil or stub connection).
var (
	globalThebeHandle   store.ThebeHandle
	globalThebeHandleMu sync.RWMutex
)

// SetGlobalHandle registers the process-wide ThebeHandle.
// Call exactly once from main after backend.New() is constructed.
func SetGlobalHandle(h store.ThebeHandle) {
	globalThebeHandleMu.Lock()
	globalThebeHandle = h
	globalThebeHandleMu.Unlock()
}

// getHandle extracts a store.ThebeHandle from conn, falling back to the
// process-wide global handle when the connection client is not a ThebeHandle.
// Returns an error only when neither source is available.
func getHandle(conn *config.PooledConnection) (store.ThebeHandle, error) {
	// Check the Handle field first (ThebeDB-backed connections).
	if conn != nil && conn.Handle != nil {
		if h, ok := conn.Handle.(store.ThebeHandle); ok {
			return h, nil
		}
	}
	globalThebeHandleMu.RLock()
	h := globalThebeHandle
	globalThebeHandleMu.RUnlock()
	if h != nil {
		return h, nil
	}
	return nil, fmt.Errorf("getHandle: no ThebeHandle available (conn=%v)", conn)
}

// storeAccountFromStore converts a store.Account to a DB_OPs Account.
// Copies ALL fields including TxNonce and TxCountSent (store.Account has both —
// see store/types.go). The prior "they default to zero" comment was false and
// silently regressed nonces on the authoritative recon path, which reads its
// base through this converter and then writes it merge-bypassing (STO-01).
// Used by BulkGetAccounts.go and any ThebeDB-backed account retrieval path.
func storeAccountFromStore(a *store.Account) *Account {
	if a == nil {
		return nil
	}
	return &Account{
		DIDAddress:  a.DIDAddress,
		Address:     a.Address,
		Balance:     a.Balance,
		Nonce:       a.Nonce,
		TxNonce:     a.TxNonce,
		TxCountSent: a.TxCountSent,
		AccountType: a.AccountType,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		Metadata:    a.Metadata,
	}
}

// storeAccountToStore converts a DB_OPs Account to a store.Account.
// TxNonce and TxCountSent are silently dropped (not in store.Account).
func storeAccountToStore(a *Account) *store.Account {
	if a == nil {
		return nil
	}
	return &store.Account{
		DIDAddress:  a.DIDAddress,
		Address:     common.Address(a.Address),
		Balance:     a.Balance,
		Nonce:       a.Nonce,
		AccountType: a.AccountType,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		Metadata:    a.Metadata,
	}
}
