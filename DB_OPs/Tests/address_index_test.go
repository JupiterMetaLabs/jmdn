package DB_OPs_Tests

import (
    "context"
    "gossipnode/DB_OPs"
    "gossipnode/config"
    "testing"
    "time"
)

func TestIndexAllTransactionAddressesReverse(t *testing.T) {
    // Initialize the main DB pool
    err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig())
    if err != nil {
        t.Fatalf("Failed to initialize the main DB pool: %v", err)
    }
    defer DB_OPs.CloseMainDBPool()

    // Get a connection from the pool
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
    if err != nil {
        t.Fatalf("Failed to get main DB connection: %v", err)
    }

    // Get the latest block number
    latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(conn)
    if err != nil {
        t.Fatalf("Failed to get latest block number: %v", err)
    }

    t.Logf("Starting to index transactions from block %d to 0", latestBlockNumber)

    // Process each block in reverse order
    var totalTransactions, totalAddresses int
    startTime := time.Now()
    const batchSize = 1000

    // Start from the latest block and go backwards
    for blockNumber := latestBlockNumber; ; blockNumber-- {
        // Process in batches for better logging
        if blockNumber%1000 == 0 || blockNumber == latestBlockNumber {
            t.Logf("Processing block %d...", blockNumber)
        }

        // Get the block
        block, err := DB_OPs.GetZKBlockByNumber(conn, blockNumber)
        if err != nil {
            if err.Error() == "not found" || err.Error() == "block not found" {
                t.Logf("Reached end of blockchain at block %d", blockNumber)
                break
            }
            t.Errorf("Failed to get block %d: %v", blockNumber, err)
            continue
        }

        if block == nil {
            t.Logf("Block %d not found - end of chain", blockNumber)
            break
        }

        // Process each transaction in the block
        for txIndex, tx := range block.Transactions {
            // Create a copy of the transaction to avoid modifying the original
            txCopy := tx
            
            // Index the transaction addresses
            err := DB_OPs.IndexTransactionAddresses(conn, blockNumber, &txCopy, txIndex)
            if err != nil {
                t.Errorf("Failed to index transaction %s in block %d: %v", 
                    tx.Hash.Hex(), blockNumber, err)
                continue
            }

            // Count the addresses (from + to)
            totalAddresses++ // from address
            if txCopy.To != nil {
                totalAddresses++ // to address (if exists)
            }
        }

        totalTransactions += len(block.Transactions)

        // Stop when we reach block 0
        if blockNumber == 0 {
            break
        }
    }

    // Log final statistics
    elapsed := time.Since(startTime)
    t.Logf("Completed indexing %d blocks with %d transactions and %d address entries in %v",
        latestBlockNumber+1, totalTransactions, totalAddresses, elapsed)
}