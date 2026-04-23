package SmartContract

import (
	"context"
	"fmt"
	"sync"
	"time"

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/pkg/types"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ============================================================================
// Shared Registry — process-wide singleton used by gossip receive path
// ============================================================================

var (
	sharedRegistry   contract_registry.RegistryDB
	sharedRegistryMu sync.RWMutex
)

// SetSharedRegistry wires the process-wide contract registry so that gossip
// messages received over ContractPropagationProtocol can persist metadata.
// Called once during server initialisation (server_integration.go).
func SetSharedRegistry(reg contract_registry.RegistryDB) {
	sharedRegistryMu.Lock()
	sharedRegistry = reg
	sharedRegistryMu.Unlock()
}

// RegisterContractFromGossip stores contract metadata received via gossip into
// the local registry.  Idempotent — if the contract already exists the call is
// a no-op (the registry records the address; bytecode is already in PebbleDB
// from EVM execution during block processing).
func RegisterContractFromGossip(
	ctx context.Context,
	addr common.Address,
	deployer common.Address,
	txHash common.Hash,
	blockNumber uint64,
	abi string,
) error {
	sharedRegistryMu.RLock()
	reg := sharedRegistry
	sharedRegistryMu.RUnlock()

	if reg == nil {
		return fmt.Errorf("SmartContract: registry not initialised, contract %s dropped — call SetSharedRegistry at startup", addr.Hex())
	}

	// Already registered — nothing to do.
	if exists, err := reg.ContractExists(ctx, addr); err == nil && exists {
		return nil
	}

	meta := &types.ContractMetadata{
		Address:      addr,
		Deployer:     deployer,
		DeployTxHash: txHash,
		DeployBlock:  blockNumber,
		DeployTime:   uint64(time.Now().UTC().Unix()),
		ABI:          abi,
		State:        "active",
	}
	return reg.RegisterContract(ctx, meta)
}

// GetContractABI retrieves the ABI string for a deployed contract from the
// local registry.  Returns ("", false) when the registry is uninitialised or
// the contract is not found.  Used by the sequencer to populate ContractMessage.
func GetContractABI(addr common.Address) (string, bool) {
	sharedRegistryMu.RLock()
	reg := sharedRegistry
	sharedRegistryMu.RUnlock()

	if reg == nil {
		return "", false
	}

	meta, err := reg.GetContract(context.Background(), addr)
	if err != nil || meta == nil {
		return "", false
	}
	return meta.ABI, meta.ABI != ""
}

// GetContractMeta returns the full registry metadata for a contract.
// Returns (meta, true) when found, (nil, false) otherwise.
// Used by the pull-on-demand responder to populate a ContractPullResponse.
func GetContractMeta(addr common.Address) (*types.ContractMetadata, bool) {
	sharedRegistryMu.RLock()
	reg := sharedRegistry
	sharedRegistryMu.RUnlock()

	if reg == nil {
		return nil, false
	}

	meta, err := reg.GetContract(context.Background(), addr)
	if err != nil || meta == nil {
		return nil, false
	}
	return meta, true
}

// HasCode returns true if the given address has contract bytecode persisted.
// Uses the shared KVStore directly — no full StateDB allocation needed.
// Safe to call from BlockProcessing hot paths.
func HasCode(addr common.Address) bool {
	return contractDB.HasCode(addr)
}

// GetCodeBytes returns the raw EVM bytecode stored for a contract address.
// Returns (nil, false) when no bytecode is found.
// Used by the pull-on-demand server to include bytecode in ContractPullResponse.
func GetCodeBytes(addr common.Address) ([]byte, bool) {
	return contractDB.GetCodeBytes(addr)
}

// StoreCodeBytes persists raw EVM bytecode for a contract address into the
// local KVStore.  Called by the pull-on-demand client after receiving bytecode
// from a peer so that HasCode returns true and contract execution can proceed.
func StoreCodeBytes(addr common.Address, code []byte) error {
	return contractDB.StoreCodeBytes(addr, code)
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
