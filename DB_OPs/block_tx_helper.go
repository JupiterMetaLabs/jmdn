package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
)

// GetTransactionsOfBlock retrieves all transactions for a given block number.
func GetTransactionsOfBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Transaction, error) {
	_, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	zkblock, err := GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsOfBlock: failed to get zkblock %d: %w", blockNumber, err)
	}

	transactions := make([]*config.Transaction, len(zkblock.Transactions))
	for i := range zkblock.Transactions {
		tx := zkblock.Transactions[i]
		transactions[i] = &tx
	}
	return transactions, nil
}
