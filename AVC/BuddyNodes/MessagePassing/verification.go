package MessagePassing

import (
	"fmt"

	"gossipnode/config"
	BlockProcessing "gossipnode/messaging/BlockProcessing"
)

// VerifyBlockExecution re-executes all transactions in a block to verify correctness
// This is used by buddy nodes during BFT consensus to validate blocks before voting
// Returns true if all transactions execute successfully, false otherwise
func VerifyBlockExecution(block *config.ZKBlock, accountsClient *config.PooledConnection) (bool, error) {
	fmt.Printf("🔍 Verifying block execution: %s\n", block.BlockHash.Hex())

	// Re-execute all transactions using ProcessBlockTransactions with commitToDB=false
	// This ensures read-only verification without modifying the local database
	err := BlockProcessing.ProcessBlockTransactions(block, accountsClient, false)

	if err != nil {
		fmt.Printf("❌ Block verification failed: %v\n", err)
		return false, fmt.Errorf("block execution verification failed: %w", err)
	}

	fmt.Printf("✅ Block verification successful\n")
	return true, nil
}
