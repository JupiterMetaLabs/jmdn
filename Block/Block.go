package Block

import (
    // "encoding/json"
    "fmt"
    "gossipnode/messaging/BlockProcessing"
    "gossipnode/DB_OPs"
    "gossipnode/config"
    "github.com/rs/zerolog/log"
)



// ProcessZKBlock processes a ZK block, updating account balances
func ProcessAndStoreZKBlock(block *config.ZKBlock, mainDBClient, accountsClient *config.ImmuClient) error {
    log.Info().
        Uint64("block_number", block.BlockNumber).
        Str("block_hash", block.BlockHash.Hex()).
        Int("txn_count", len(block.Transactions)).
        Msg("Processing and storing ZK block")

    // 1. First store the block in the main database
    if err := DB_OPs.StoreZKBlock(mainDBClient, block); err != nil {
        return fmt.Errorf("failed to store block: %w", err)
    }

    // 2. Process each transaction to update account balances
    for i, tx := range block.Transactions {
        if err := BlockProcessing.ProcessTransaction(tx, block.CoinbaseAddr, block.ZKVMAddr, accountsClient); err != nil {
            log.Error().
                Err(err).
                Str("tx_hash", tx.Hash).
                Int("tx_index", i).
                Msg("Failed to process transaction")
            return fmt.Errorf("failed to process transaction %s: %w", tx.Hash, err)
        }
    }

    log.Info().
        Uint64("block_number", block.BlockNumber).
        Str("block_hash", block.BlockHash.Hex()).
        Msg("ZK block processed and stored successfully")
    
	return nil
}