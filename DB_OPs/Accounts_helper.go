package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
)

const (
	MainImmuConn     = 0
	AccountsImmuConn = 1
)

func GetImmuClient() (*config.PooledConnection, error) {
	var err error
	var PooledConnection *config.PooledConnection
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database connection: %w - GetImmuClient", err)
	}
	return PooledConnection, nil
}

func CloseImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	PutMainDBConnection(PooledConnection)
	return nil
}

func GetAccountsImmuClient() (*config.PooledConnection, error) {
	var err error
	var PooledConnection *config.PooledConnection
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	PooledConnection, err = GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts database connection: %w - GetAccountsImmuClient", err)
	}
	return PooledConnection, nil
}

func CloseAccountsImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	PutAccountsConnection(PooledConnection)
	return nil
}

// GetCountofRecords is an ImmuDB-specific helper that has been superseded by ThebeDB queries.
// Returns 0 with an error — callers should migrate to ThebeDB equivalents (Phase 6).
func GetCountofRecords(PooledConnection *config.PooledConnection, ConnType int, prefix string) (int, error) {
	return 0, fmt.Errorf("GetCountofRecords: not available with ThebeDB backend (migrate to store.BlockStore/AccountStore query in Phase 6)")
}

// Builder function to get the count of records using the Native ImmuDB API
type CountBuilder struct{}

func (cb CountBuilder) Build() (*CountBuilder, error) {
	return &CountBuilder{}, nil
}

func (cb CountBuilder) GetMainDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, MainImmuConn, prefix)
}

func (cb CountBuilder) GetAccountsDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, AccountsImmuConn, prefix)
}
