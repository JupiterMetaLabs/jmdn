package DB_OPs

import (
	"fmt"
	"gossipnode/config"
	AppContext "gossipnode/config/Context"
	"gossipnode/logging"
	"time"

	"go.uber.org/zap"
)

// Get all the transactions of a block
func GetTransactionsOfBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Transaction, error) {
	var err error
	var shouldReturnConnection bool = false

	// Define Function wide context for timeout
	ctx, cancel := AppContext.GetAppContext(MainDBAppContext).NewChildContextWithTimeout(10*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),

			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetTransactionsOfBlock"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, "ImmuDB.log"),
				zap.String(logging.Topic, "ImmuDB_ImmuClient"),
				zap.String(logging.Loki_url, logging.GetLokiURL()),
				zap.String(logging.Function, "DB_OPs.GetTransactionsOfBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	// First get the zkblock
	zkblock, err := GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get zkblock of block",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, "ImmuDB.log"),
			zap.String(logging.Topic, "ImmuDB_ImmuClient"),
			zap.String(logging.Loki_url, logging.GetLokiURL()),
			zap.String(logging.Function, "DB_OPs.GetTransactionsOfBlock"),
		)
		return nil, fmt.Errorf("failed to get zkblock of block: %w", err)
	}

	transactions := make([]*config.Transaction, len(zkblock.Transactions))
	for i, tx := range zkblock.Transactions {
		transactions[i] = &tx
	}
	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved transactions of block",
		zap.Uint64("blockNumber", blockNumber),
		zap.Int("transactionCount", len(transactions)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, "ImmuDB.log"),
		zap.String(logging.Topic, "ImmuDB_ImmuClient"),
		zap.String(logging.Loki_url, logging.GetLokiURL()),
		zap.String(logging.Function, "DB_OPs.GetTransactionsOfBlock"),
	)
	return transactions, nil
}
