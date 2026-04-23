package types

import (
	"github.com/ethereum/go-ethereum/common"
)

// CompiledContract represents a compiled smart contract
type CompiledContract struct {
	// Source code
	SourceCode string `json:"source_code,omitempty"`

	// Compiled bytecode (creation bytecode)
	Bytecode []byte `json:"bytecode"`

	// Runtime bytecode (deployed code)
	RuntimeBytecode []byte `json:"runtime_bytecode,omitempty"`

	// Contract ABI (Application Binary Interface)
	ABI string `json:"abi"`

	// Compiler version used
	CompilerVersion string `json:"compiler_version,omitempty"`

	// Compiler optimization settings
	OptimizationRuns uint32 `json:"optimization_runs,omitempty"`

	// Bytecode hash for verification
	BytecodeHash common.Hash `json:"bytecode_hash"`

	// Metadata (e.g., source mappings, devdoc, userdoc)
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ContractMetadata represents deployed contract metadata
// This is stored in contractsdb registry
type ContractMetadata struct {
	// Contract address
	Address common.Address `json:"address"`

	// Deployer address
	Deployer common.Address `json:"deployer"`

	// Contract name (optional)
	Name string `json:"name,omitempty"`

	// Contract ABI
	ABI string `json:"abi,omitempty"`

	// Bytecode hash for verification
	BytecodeHash common.Hash `json:"bytecode_hash"`

	// Deployment information
	DeployBlock  uint64      `json:"deploy_block"`
	DeployTime   uint64      `json:"deploy_time"`
	DeployTxHash common.Hash `json:"deploy_tx_hash"`

	// Contract size and complexity
	CodeSize     uint64 `json:"code_size"`
	StorageSlots uint64 `json:"storage_slots,omitempty"`

	// Contract type classification
	ContractType string `json:"contract_type,omitempty"` // erc20, erc721, erc1155, custom

	// Compilation information
	CompilerVersion  string `json:"compiler_version,omitempty"`
	OptimizationRuns uint32 `json:"optimization_runs,omitempty"`

	// Contract state
	State string `json:"state"` // active, paused, destroyed

	// Additional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}
