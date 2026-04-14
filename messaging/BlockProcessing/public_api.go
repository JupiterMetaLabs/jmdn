package BlockProcessing

import (
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ProcessSingleTransaction is a public wrapper around processTransaction
// This allows external packages (e.g., buddy nodes) to re-execute individual transactions
// during block verification.
//
// Parameters:
//   - tx: The transaction to process
//   - coinbaseAddr: Address to receive coinbase portion of gas fees
//   - zkvmAddr: Address to receive ZKVM portion of gas fees
//   - accountsClient: Database connection for account operations
//   - commitToDB: If true, persist state changes; if false, verification mode (read-only)
//
// Returns error if transaction processing fails
func ProcessSingleTransaction(
	tx *config.Transaction,
	coinbaseAddr common.Address,
	zkvmAddr common.Address,
	accountsClient *config.PooledConnection,
	commitToDB bool,
) error {
	// Discard the ContractDeploymentInfo — buddy-node verification paths don't propagate.
	_, err := processTransaction(*tx, coinbaseAddr, zkvmAddr, accountsClient, commitToDB)
	return err
}
