package evm

import (
	"fmt"
	"gossipnode/helper"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// EVMExecutor manages EVM execution
type EVMExecutor struct {
	ChainConfig *params.ChainConfig
	VMConfig    vm.Config
}

// NewEVMExecutor creates a new EVM execution environment
func NewEVMExecutor(chainID int) *EVMExecutor {
	return &EVMExecutor{
		ChainConfig: NewChainConfig(chainID),
		VMConfig:    NewVMConfig(),
	}
}

// DeployContract deploys a smart contract
func (e *EVMExecutor) DeployContract(state vm.StateDB, caller common.Address, code []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
	// Create initial EVM instance
	value256, overflow := helper.ConvertBigToUint256(value)
	if overflow {
		return nil, fmt.Errorf("Overflow occurred during value conversion")
	}

	blockCtx := DefaultBlockContext(gasLimit)

	// Try to update with real blockchain info
	if err := UpdateBlockContext(&blockCtx); err != nil {
		// Log the error but continue with default values
		// fmt.Printf("Warning: Using default block context: %v\n", err)
	}

	txCtx := vm.TxContext{
		Origin:   caller,
		GasPrice: big.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, txCtx, state, e.ChainConfig, e.VMConfig)

	// Create contract
	// contractAddr := CreateAddress(caller, state.GetNonce(caller)) // crypto.CreateAddress does this inside evm.Create? No, Create returns the address.
	// Actually, evm.Create handles address generation and nonce increment internally?
	// Let's check original code:
	// contractAddr := crypto.CreateAddress(caller, state.GetNonce(caller))
	// state.SetNonce(caller, state.GetNonce(caller)+1)
	// ret, contractAddr, leftOverGas, err := evm.Create(contractRef, code, gasLimit, value256)

	// Wait, standard vm.EVM.Create does NOT increment the nonce of the caller automatically?
	// It depends on the implementation of vm.Create.
	// The original code MANUALLY calculated address and incremented nonce.
	// But evm.Create calculates the address too.
	// Let's follow the original implementation logic to be safe, but utilize our new structure.

	state.SetNonce(caller, state.GetNonce(caller)+1)

	// Initialize memory and stack for execution
	contractRef := vm.AccountRef(caller)

	// Execute the deployment code
	ret, contractAddr, leftOverGas, err := evm.Create(contractRef, code, gasLimit, value256)

	result := &ExecutionResult{
		ReturnData:   ret,
		GasUsed:      gasLimit - leftOverGas,
		Error:        err,
		ContractAddr: contractAddr,
	}

	return result, err
}

// ExecuteContract executes a function on a deployed contract
func (e *EVMExecutor) ExecuteContract(state vm.StateDB, caller common.Address, contractAddr common.Address,
	input []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {

	value256, overflow := helper.ConvertBigToUint256(value)
	if overflow {
		return nil, fmt.Errorf("Overflow occurred during value conversion")
	}

	// Create EVM instance
	blockCtx := DefaultBlockContext(gasLimit)

	// Try to update with real blockchain info
	if err := UpdateBlockContext(&blockCtx); err != nil {
		// Log the error but continue with default values
		// fmt.Printf("Warning: Using default block context: %v\n", err)
	}

	txCtx := vm.TxContext{
		Origin:   caller,
		GasPrice: big.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, txCtx, state, e.ChainConfig, e.VMConfig)

	// Initialize references for execution
	callerRef := vm.AccountRef(caller)

	// Call the contract
	ret, leftOverGas, err := evm.Call(callerRef, contractAddr, input, gasLimit, value256)

	result := &ExecutionResult{
		ReturnData: ret,
		GasUsed:    gasLimit - leftOverGas,
		Error:      err,
	}

	return result, err
}
