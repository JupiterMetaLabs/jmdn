package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ============================================================================
// Data Structures for Persistence (PebbleDB -> Future SQL)
// ============================================================================

// ContractMetadata represents the immutable metadata of a deployed smart contract.
// Maps to SQL table: 'contracts'
type ContractMetadata struct {
	ContractAddress  common.Address `json:"contract_address"`
	CodeHash         common.Hash    `json:"code_hash"`
	CodeSize         uint64         `json:"code_size"`
	DeployerAddress  common.Address `json:"deployer_address"`
	DeploymentTxHash common.Hash    `json:"deployment_tx_hash"`
	DeploymentBlock  uint64         `json:"deployment_block"`
	CreatedAt        int64          `json:"created_at"` // Unix timestamp
}

// TransactionReceipt represents the result of a transaction execution.
// Maps to SQL table: 'transaction_receipts'
type TransactionReceipt struct {
	TxHash          common.Hash    `json:"tx_hash"`
	BlockNumber     uint64         `json:"block_number"`
	TxIndex         uint64         `json:"tx_index"`
	Status          uint64         `json:"status"` // 1 = Success, 0 = Fail
	GasUsed         uint64         `json:"gas_used"`
	ContractAddress common.Address `json:"contract_address,omitempty"` // For deployments
	Logs            []*types.Log   `json:"logs"`
	RevertReason    string         `json:"revert_reason,omitempty"`
	CreatedAt       int64          `json:"created_at"`
}

// StorageMetadata represents metadata for a specific storage slot update.
// Maps to SQL table: 'contract_storage'
// Note: This is stored separately from the raw value to avoid overhead on hot paths.
type StorageMetadata struct {
	ContractAddress   common.Address `json:"contract_address"`
	StorageKey        common.Hash    `json:"storage_key"`
	ValueHash         common.Hash    `json:"value_hash"`
	LastModifiedBlock uint64         `json:"last_modified_block"`
	LastModifiedTx    common.Hash    `json:"last_modified_tx"`
	UpdatedAt         int64          `json:"updated_at"`
}
