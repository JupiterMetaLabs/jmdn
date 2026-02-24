package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
)

// Get all the transactions of a block
func GetTransactionsOfBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Transaction, error) {
	var err error
	var shouldReturnConnection bool = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetTransactionsOfBlock"))
	}
	if shouldReturnConnection {
		defer func() {
			loggerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", "ImmuDB.log"),
				ion.String("topic", "ImmuDB_ImmuClient"),
				ion.String("function", "DB_OPs.GetTransactionsOfBlock"))
			PutMainDBConnection(mainDBClient)
		}()
	}

	// First get the zkblock
	zkblock, err := GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get zkblock of block",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", "ImmuDB.log"),
			ion.String("topic", "ImmuDB_ImmuClient"),
			ion.String("function", "DB_OPs.GetTransactionsOfBlock"))
		return nil, fmt.Errorf("failed to get zkblock of block: %w", err)
	}

	transactions := make([]*config.Transaction, len(zkblock.Transactions))
	for i, tx := range zkblock.Transactions {
		transactions[i] = &tx
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved transactions of block",
		ion.Uint64("blockNumber", blockNumber),
		ion.Int("transactionCount", len(transactions)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", "ImmuDB.log"),
		ion.String("topic", "ImmuDB_ImmuClient"),
		ion.String("function", "DB_OPs.GetTransactionsOfBlock"))
	return transactions, nil
}
