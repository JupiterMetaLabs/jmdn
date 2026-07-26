package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully retrieved Main DB Connection",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetImmuClient"))
	return PooledConnection, nil
}

func CloseImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Put back the Main DB Connection to the Pool",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.CloseImmuClient"))
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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully retrieved Accounts DB Connection",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetAccountsImmuClient"))
	return PooledConnection, nil
}

func CloseAccountsImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Put back the Accounts DB Connection to the Pool",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.CloseAccountsImmuClient"))
	PutAccountsConnection(PooledConnection)
	return nil
}

// Adapter function to get the count of records using the Native ImmuDB API
// Get the count of accounts uisng immudb native apis
// GetCountofRecords counts keys with the given prefix via immudb's Count API.
// countTimeout bounds the Count RPC; pass <= 0 for the default 30s. Large
// prefixes (e.g. all accounts) can exceed 30s, so off-request-path callers (the
// one-time stats seed) pass a longer budget.
func GetCountofRecords(PooledConnection *config.PooledConnection, ConnType int, prefix string, countTimeout time.Duration) (int, error) {
	var err error
	var shouldReturnConnection = false

	switch ConnType {
	case MainImmuConn:
		if PooledConnection == nil || PooledConnection.Client == nil {
			PooledConnection, err = GetImmuClient()
			if err != nil {
				return 0, fmt.Errorf("failed to get main database connection: %w - GetCountofRecords", err)
			}
			shouldReturnConnection = true
			defer func() {
				if shouldReturnConnection {
					CloseImmuClient(PooledConnection)
				}
			}()
		}
	case AccountsImmuConn:
		if PooledConnection == nil || PooledConnection.Client == nil {
			PooledConnection, err = GetAccountsImmuClient()
			if err != nil {
				return 0, fmt.Errorf("failed to get accounts database connection: %w - GetCountofRecords", err)
			}
			shouldReturnConnection = true
			defer func() {
				if shouldReturnConnection {
					CloseAccountsImmuClient(PooledConnection)
				}
			}()
		}
	}

	// Now call the appropriate function to get the count of records
	// Use the Native ImmuDB Count API to efficiently count keys with the given prefix

	// Ensure the appropriate database is selected based on connection type
	if ConnType == MainImmuConn {
		if err := ensureMainDBSelected(PooledConnection); err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to ensure main database is selected",
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetCountofRecords"))
			return 0, fmt.Errorf("failed to ensure main database is selected: %w", err)
		}
	} else if ConnType == AccountsImmuConn {
		if err := ensureAccountsDBSelected(PooledConnection); err != nil {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to ensure accounts database is selected",
				err,
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetCountofRecords"))
			return 0, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
		}
	}

	if countTimeout <= 0 {
		countTimeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), countTimeout)
	defer cancel()

	// Use immudb Count API — much faster and simpler
	countResp, err := PooledConnection.Client.Client.Count(ctx, []byte(prefix))
	if err != nil {
		dbName := config.DBName
		if ConnType == AccountsImmuConn {
			dbName = config.AccountsDBName
		}
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to count keys",
			err,
			ion.String("prefix", prefix),
			ion.String("database", dbName),
			ion.String("function", "DB_OPs.GetCountofRecords"))
		return 0, fmt.Errorf("failed to count keys: %w", err)
	}

	totalKeys := int(countResp.Count)

	dbName := config.DBName
	if ConnType == AccountsImmuConn {
		dbName = config.AccountsDBName
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PooledConnection.Client.Logger.Debug(loggerCtx, "Total keys counted with prefix",
		ion.Int("count", totalKeys),
		ion.String("prefix", prefix),
		ion.String("database", dbName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("function", "DB_OPs.GetCountofRecords"))

	return totalKeys, nil
}

// Builder function to get the count of records using the Native ImmuDB API
type CountBuilder struct{}

func (cb CountBuilder) Build() (*CountBuilder, error) {
	return &CountBuilder{}, nil
}

func (cb CountBuilder) GetMainDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, MainImmuConn, prefix, 30*time.Second)
}

func (cb CountBuilder) GetAccountsDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, AccountsImmuConn, prefix, 30*time.Second)
}

// GetAccountsDBCountWithTimeout is GetAccountsDBCount with a caller-chosen
// deadline for the immudb Count — used by the one-time stats seed, which runs
// off the request path and can allow minutes on a large accounts DB.
func (cb CountBuilder) GetAccountsDBCountWithTimeout(prefix string, countTimeout time.Duration) (int, error) {
	return GetCountofRecords(nil, AccountsImmuConn, prefix, countTimeout)
}
