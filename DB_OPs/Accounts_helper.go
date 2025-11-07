package DB_OPs

import (
	"context"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"
	"time"

	"go.uber.org/zap"
)

const (
	MainImmuConn     = 0
	AccountsImmuConn = 1
)

func GetImmuClient() (*config.PooledConnection, error) {
	var err error
	var PooledConnection *config.PooledConnection

	PooledConnection, err = GetMainDBConnection()
	if err != nil {
		return nil, err
	}
	PooledConnection.Client.Logger.Logger.Info("Successfully retrieved Main DB Connection",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetImmuClient"),
	)
	return PooledConnection, nil
}

func CloseImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	PooledConnection.Client.Logger.Logger.Info("Put back the Main DB Connection to the Pool",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.CloseImmuClient"),
	)
	PutMainDBConnection(PooledConnection)
	return nil
}

func GetAccountsImmuClient() (*config.PooledConnection, error) {
	var err error
	var PooledConnection *config.PooledConnection
	PooledConnection, err = GetAccountsConnection()
	if err != nil {
		return nil, err
	}
	PooledConnection.Client.Logger.Logger.Info("Successfully retrieved Accounts DB Connection",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetAccountsImmuClient"),
	)
	return PooledConnection, nil
}

func CloseAccountsImmuClient(PooledConnection *config.PooledConnection) error {
	if PooledConnection == nil || PooledConnection.Client == nil {
		return nil
	}
	PooledConnection.Client.Logger.Logger.Info("Put back the Accounts DB Connection to the Pool",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.CloseAccountsImmuClient"),
	)
	PutAccountsConnection(PooledConnection)
	return nil
}

// Adapter function to get the count of records using the Native ImmuDB API
// Get the count of accounts uisng immudb native apis
func GetCountofRecords(PooledConnection *config.PooledConnection, ConnType int, prefix string) (int, error) {
	var err error
	var shouldReturnConnection bool = false

	switch ConnType {
	case MainImmuConn:
		if PooledConnection == nil || PooledConnection.Client == nil {
			PooledConnection, err = GetImmuClient()
			if err != nil {
				return 0, err
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
				return 0, err
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
			PooledConnection.Client.Logger.Logger.Error("Failed to ensure main database is selected",
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetCountofRecords"),
			)
			return 0, fmt.Errorf("failed to ensure main database is selected: %w", err)
		}
	} else if ConnType == AccountsImmuConn {
		if err := ensureAccountsDBSelected(PooledConnection); err != nil {
			PooledConnection.Client.Logger.Logger.Error("Failed to ensure accounts database is selected",
				zap.Error(err),
				zap.String(logging.Connection_database, config.AccountsDBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetCountofRecords"),
			)
			return 0, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use immudb Count API — much faster and simpler
	countResp, err := PooledConnection.Client.Client.Count(ctx, []byte(prefix))
	if err != nil {
		dbName := config.DBName
		if ConnType == AccountsImmuConn {
			dbName = config.AccountsDBName
		}
		PooledConnection.Client.Logger.Logger.Error("Failed to count keys",
			zap.Error(err),
			zap.String("prefix", prefix),
			zap.String(logging.Connection_database, dbName),
			zap.String(logging.Function, "DB_OPs.GetCountofRecords"),
		)
		return 0, fmt.Errorf("failed to count keys: %w", err)
	}

	totalKeys := int(countResp.Count)

	dbName := config.DBName
	if ConnType == AccountsImmuConn {
		dbName = config.AccountsDBName
	}

	PooledConnection.Client.Logger.Logger.Info("Total keys counted with prefix",
		zap.Int("count", totalKeys),
		zap.String("prefix", prefix),
		zap.String(logging.Connection_database, dbName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Function, "DB_OPs.GetCountofRecords"),
	)

	return totalKeys, nil
}

// Builder function to get the count of records using the Native ImmuDB API
type CountBuilder struct {}

func (cb CountBuilder) Build() (*CountBuilder, error) {
	return &CountBuilder{}, nil
}

func (cb CountBuilder) GetMainDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, MainImmuConn, prefix)
}

func (cb CountBuilder) GetAccountsDBCount(prefix string) (int, error) {
	return GetCountofRecords(nil, AccountsImmuConn, prefix)
}