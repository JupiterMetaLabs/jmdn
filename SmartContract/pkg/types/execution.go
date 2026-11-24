package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ExecutionResult represents the result of a contract execution
type ExecutionResult struct {
	// Return data from the contract execution
	ReturnData []byte `json:"return_data"`

	// Contract address (for deployments)
	ContractAddress common.Address `json:"contract_address,omitempty"`

	// Gas used during execution
	GasUsed uint64 `json:"gas_used"`

	// Gas limit provided
	GasLimit uint64 `json:"gas_limit"`

	// Whether execution was successful
	Success bool `json:"success"`

	// Error message if execution failed
	ErrorMessage string `json:"error_message,omitempty"`

	// Revert reason if contract reverted
	RevertReason string `json:"revert_reason,omitempty"`

	// Logs generated during execution
	Logs []Log `json:"logs,omitempty"`

	// State changes (for debugging)
	StateChanges map[string]interface{} `json:"state_changes,omitempty"`
}

// DeploymentResult represents the result of a contract deployment
type DeploymentResult struct {
	// Embedded execution result
	*ExecutionResult

	// Deployed contract address
	Address common.Address `json:"address"`

	// Deployer address
	Deployer common.Address `json:"deployer"`

	// Block number when deployed
	BlockNumber uint64 `json:"block_number"`

	// Transaction hash
	TxHash common.Hash `json:"tx_hash"`

	// Code size of deployed contract
	CodeSize uint64 `json:"code_size"`
}

// Log represents an event log from contract execution
type Log struct {
	Address common.Address `json:"address"`
	Topics  []common.Hash  `json:"topics"`
	Data    []byte         `json:"data"`

	// Block and transaction info
	BlockNumber uint64      `json:"block_number"`
	TxHash      common.Hash `json:"tx_hash"`
	TxIndex     uint        `json:"tx_index"`
	LogIndex    uint        `json:"log_index"`
	Removed     bool        `json:"removed"`
}

// CallResult represents the result of a read-only contract call
type CallResult struct {
	// Return data from the call
	ReturnData []byte `json:"return_data"`

	// Gas used (estimated)
	GasUsed uint64 `json:"gas_used"`

	// Whether call was successful
	Success bool `json:"success"`

	// Error message if call failed
	ErrorMessage string `json:"error_message,omitempty"`
}

// GasEstimate represents gas estimation for a transaction
type GasEstimate struct {
	// Estimated gas required
	GasRequired uint64 `json:"gas_required"`

	// Recommended gas limit (with buffer)
	GasLimit uint64 `json:"gas_limit"`

	// Gas price (wei per gas)
	GasPrice *big.Int `json:"gas_price,omitempty"`

	// Total cost estimate (gas * price)
	TotalCost *big.Int `json:"total_cost,omitempty"`
}
