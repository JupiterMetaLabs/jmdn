// MODULE: DB_OPs/compat_connections.go
// PURPOSE: Compatibility shims for legacy ImmuDB connection pool functions.
//
// BACKGROUND:
//   The remove/immudb branch deleted Account_Connections.go and
//   MainDB_Connections.go. The merged immuclient.go and account_immuclient.go
//   still reference the functions that were defined in those files.
//   These stubs let the merged code compile while the migration is in progress.
//
// TO COMPLETE MIGRATION:
//   Replace each stub body with a call through getHandle() or remove the caller.
//   Once no caller references these functions, this file can be deleted.
//
// DO NOT:
//   - Use these stubs for new production code. Use getHandle() instead.
//   - Add new business logic here.

package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"
)

// DatabaseState replaces the former schema.ImmutableState alias.
// ImmuDB is removed; callers receive a zeroed state from GetDatabaseState.
type DatabaseState struct {
	Db     string
	TxId   uint64
	TxHash []byte
}

// ---------------------------------------------------------------------------
// MainDB connection pool shims (were in MainDB_Connections.go)
// ---------------------------------------------------------------------------

// GetMainDBConnectionandPutBack returns a nil connection WITHOUT error.
// The nil conn is the codebase-wide "use the process handle" sentinel: every
// storage helper passes it to getHandle(conn), which resolves the global
// ThebeHandle (and fails loud there if Thebe is not initialized). Returning an
// error here instead would break every legacy call-site that treats
// acquisition failure as fatal (node boot, block receive path, tx validation)
// even though those paths no longer need a pooled connection at all.
//
// Deprecated: migrate callers to use getHandle() directly.
func GetMainDBConnectionandPutBack(_ context.Context) (*config.PooledConnection, error) {
	return nil, nil
}

// PutMainDBConnection is a no-op: the ThebeDB-backed connection pool does not
// require explicit return of connections.
//
// Deprecated: migrate callers to use getHandle() directly.
func PutMainDBConnection(_ *config.PooledConnection) {}

// ensureMainDBSelected is a no-op for ThebeDB — no database selection is needed.
//
// Deprecated: remove callers.
func ensureMainDBSelected(_ *config.PooledConnection) error { return nil }

// ---------------------------------------------------------------------------
// Accounts DB connection pool shims (were in Account_Connections.go)
// ---------------------------------------------------------------------------

// GetAccountConnectionandPutBack returns a nil connection WITHOUT error —
// same contract as GetMainDBConnectionandPutBack (nil conn = process handle).
//
// Deprecated: migrate callers to use getHandle() directly.
func GetAccountConnectionandPutBack(_ context.Context) (*config.PooledConnection, error) {
	return nil, nil
}

// PutAccountsConnection is a no-op: the ThebeDB-backed handle does not require
// explicit return.
//
// Deprecated: migrate callers to use getHandle() directly.
func PutAccountsConnection(_ *config.PooledConnection) {}

// GetAccountsConnections returns nil as the accounts pool no longer exists.
//
// Deprecated: remove callers.
func GetAccountsConnections(_ context.Context) (*config.PooledConnection, error) {
	return nil, nil
}

// storeAccountToDBOps converts a *store.Account to *Account.
func storeAccountToDBOps(a *store.Account) *Account {
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

// ListAccountsPaginatedCtx returns a page of accounts using offset-based pagination via ThebeDB.
func ListAccountsPaginatedCtx(ctx context.Context, limit, offset int) ([]*Account, error) {
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("ListAccountsPaginatedCtx: %w", err)
	}
	rows, err := h.ListAccountsPaginated(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([]*Account, len(rows))
	for i, r := range rows {
		result[i] = storeAccountToDBOps(r)
	}
	return result, nil
}

// CountAccountsCtx returns the total number of accounts via ThebeDB.
func CountAccountsCtx(ctx context.Context) (uint64, error) {
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("CountAccountsCtx: %w", err)
	}
	return h.CountAccounts(ctx)
}

// GetAccountsByNonces returns accounts matching any of the given nonces via ThebeDB.
func GetAccountsByNonces(ctx context.Context, nonces []uint64) ([]*Account, error) {
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetAccountsByNonces: %w", err)
	}
	rows, err := h.GetAccountsByNonces(ctx, nonces)
	if err != nil {
		return nil, err
	}
	result := make([]*Account, len(rows))
	for i, r := range rows {
		result[i] = storeAccountToDBOps(r)
	}
	return result, nil
}

// SaveAccount persists a full Account record — delegates to UpdateAccountBalance.
// This is a stub for Nodeinfo callers.
func SaveAccount(conn *config.PooledConnection, acc *Account) error {
	if acc == nil {
		return ErrNilValue
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("SaveAccount: %w", err)
	}

	return h.UpdateAccountBalance(ctx, acc.Address.Hex(), acc.Balance)
}

// EnsureDBConnection verifies the storage backend is reachable. ThebeDB: the
// embedded handle is process-wide — reachable iff the handle is installed.
// The conn parameter is accepted for call-site compatibility and unused.
// InitAccountsPool is a compat shim: pool construction is factory-based now
// (config.SetGlobalHandleFactory at ThebeDB init). Legacy tests call it before
// exercising account paths; those paths surface their own errors when no
// handle/DB is available, which the tests treat as skip conditions.
func InitAccountsPool() error { return nil }

// InitMainDBPool is a compat shim — see InitAccountsPool.
func InitMainDBPool(_ *config.ConnectionPoolConfig) error { return nil }

func EnsureDBConnection(_ *config.PooledConnection) error {
	if _, err := getHandle(nil); err != nil {
		return fmt.Errorf("EnsureDBConnection: %w", err)
	}
	return nil
}
