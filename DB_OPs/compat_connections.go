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

	"gossipnode/config"

	"github.com/codenotary/immudb/pkg/api/schema"
)

// DatabaseState is a legacy alias used by explorer/BlockOps.go
type DatabaseState = schema.ImmutableState

// ---------------------------------------------------------------------------
// MainDB connection pool shims (were in MainDB_Connections.go)
// ---------------------------------------------------------------------------

// GetMainDBConnectionandPutBack returns a synthetic PooledConnection whose
// Client field is nil. Legacy call-sites that need an ImmuDB connection
// will observe a nil Client and should fall back to getHandle().
//
// Deprecated: migrate callers to use getHandle() directly.
func GetMainDBConnectionandPutBack(_ context.Context) (*config.PooledConnection, error) {
	return nil, fmt.Errorf("GetMainDBConnectionandPutBack: ImmuDB pool removed — use getHandle(nil) instead")
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

// GetAccountConnectionandPutBack returns an error: the accounts ImmuDB pool
// no longer exists. Callers should use getHandle() for all account operations.
//
// Deprecated: migrate callers to use getHandle() directly.
func GetAccountConnectionandPutBack(_ context.Context) (*config.PooledConnection, error) {
	return nil, fmt.Errorf("GetAccountConnectionandPutBack: ImmuDB accounts pool removed — use getHandle(nil) instead")
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
	return nil, fmt.Errorf("GetAccountsConnections: ImmuDB accounts pool removed — use getHandle(nil) instead")
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
