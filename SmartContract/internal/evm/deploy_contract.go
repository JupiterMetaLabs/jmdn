package evm

import (
	contractDB "gossipnode/DB_OPs/contractDB"

	"github.com/ethereum/go-ethereum/common"
)

// DeploymentResult contains the result of a contract deployment.
type DeploymentResult struct {
	ContractAddress common.Address
	GasUsed         uint64
	Success         bool
	Error           error
}

// InitializeStateDB creates a StateDB for EVM execution.
// Delegates entirely to contractDB.InitializeStateDB() which manages the singletons.
func InitializeStateDB(chainID int) (contractDB.StateDB, error) {
	return contractDB.InitializeStateDB()
}
