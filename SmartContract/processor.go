package SmartContract

import (
	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// HasCode returns true if the given address has contract bytecode persisted.
// Uses the shared KVStore directly — no full StateDB allocation needed.
// Safe to call from BlockProcessing hot paths.
func HasCode(addr common.Address) bool {
	return contractDB.HasCode(addr)
}

// DeploymentResult contains the result of a contract deployment.
type DeploymentResult struct {
	ContractAddress common.Address
	GasUsed         uint64
	Success         bool
	Error           error
}

// ProcessContractDeployment is the public entry point for contract deployment.
// Called by messaging/BlockProcessing after receiving a deployment transaction.
func ProcessContractDeployment(
	tx *config.Transaction,
	stateDB StateDB,
	chainID int,
) (*DeploymentResult, error) {
	result, err := evm.ProcessContractDeployment(tx, stateDB.(contractDB.StateDB), chainID)
	if err != nil {
		return &DeploymentResult{Success: false, Error: err}, err
	}
	return &DeploymentResult{
		ContractAddress: result.ContractAddress,
		GasUsed:         result.GasUsed,
		Success:         result.Success,
		Error:           result.Error,
	}, nil
}

// ProcessContractExecution is the public entry point for contract execution.
func ProcessContractExecution(
	tx *config.Transaction,
	stateDB StateDB,
	chainID int,
) (*evm.ExecutionResult, error) {
	return evm.ProcessContractExecution(tx, stateDB.(contractDB.StateDB), chainID)
}

// ============================================================================
// Public Types & Factory Functions (Wrappers for Internal Packages)
// ============================================================================

// EVMExecutor is a wrapper/alias for the internal EVM executor
type EVMExecutor = evm.EVMExecutor

// NewEVMExecutor creates a new EVM executor instance
func NewEVMExecutor(chainID int) *EVMExecutor {
	return evm.NewEVMExecutor(chainID)
}

// NewStateDB creates a new StateDB instance with default configuration
// utilizing the underlying infrastructure (PebbleDB + gRPC clients)
func NewStateDB(chainID int) (StateDB, error) {
	return evm.InitializeStateDB(chainID)
}
