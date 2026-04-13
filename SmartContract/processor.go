package SmartContract

import (
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// DeploymentResult contains the result of a contract deployment
type DeploymentResult struct {
	ContractAddress common.Address
	GasUsed         uint64
	Success         bool
	Error           error
}

// ProcessContractDeployment is a public wrapper that calls the internal deployment logic
// This is the function that messaging/BlockProcessing can call
func ProcessContractDeployment(
	tx *config.Transaction,
	stateDB StateDB,
	chainID int,
) (*DeploymentResult, error) {
	// Call the internal implementation
	result, err := evm.ProcessContractDeployment(tx, stateDB.(state.StateDB), chainID)
	if err != nil {
		return &DeploymentResult{
			Success: false,
			Error:   err,
		}, err
	}

	return &DeploymentResult{
		ContractAddress: result.ContractAddress,
		GasUsed:         result.GasUsed,
		Success:         result.Success,
		Error:           result.Error,
	}, nil
}

// ProcessContractExecution is a public wrapper for contract execution
func ProcessContractExecution(
	tx *config.Transaction,
	stateDB StateDB,
	chainID int,
) (*evm.ExecutionResult, error) {
	return evm.ProcessContractExecution(tx, stateDB.(state.StateDB), chainID)
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
