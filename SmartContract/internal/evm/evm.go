package evm

import (
	"fmt"
	"gossipnode/helper"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// EVMExecutor manages EVM execution
type EVMExecutor struct {
	ChainConfig *params.ChainConfig
	VMConfig    vm.Config
}

var canTransferFn vm.CanTransferFunc = func(db vm.StateDB, addr common.Address, amount *uint256.Int) bool {
	balance := db.GetBalance(addr)
	return balance.Cmp(amount) >= 0
}

var transferFn vm.TransferFunc = func(db vm.StateDB, sender, recipient common.Address, amount *uint256.Int) {
	db.SubBalance(sender, amount, tracing.BalanceChangeTransfer)
	db.AddBalance(recipient, amount, tracing.BalanceChangeTransfer)
}

// NewEVMExecutor creates a new EVM execution environment
func NewEVMExecutor(chainID int) *EVMExecutor {
	return &EVMExecutor{
		ChainConfig: NewChainConfig(chainID), // Use the properly configured ChainConfig
		VMConfig:    NewVMConfig(),           // Use the properly configured VMConfig
	}
}

// DeployContract deploys a smart contract
func (e *EVMExecutor) DeployContract(state vm.StateDB, caller common.Address, code []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
	// Create initial EVM instance
	value256, overflow := helper.ConvertBigToUint256(value)
	if overflow {
		return nil, fmt.Errorf("Overflow occurred during value conversion")
	}

	blockCtx := vm.BlockContext{
		CanTransfer: canTransferFn,
		Transfer:    transferFn,
		GetHash:     GetHashFn,
		Coinbase:    common.Address{},
		BlockNumber: new(big.Int).SetUint64(1),
		Time:        uint64(time.Now().UTC().Unix()),
		Difficulty:  big.NewInt(0),
		GasLimit:    30_000_000,
		BaseFee:     big.NewInt(1000000000), // 1 gwei - must be non-zero for EIP-1559
	}

	// Try to update with real blockchain info
	if err := UpdateBlockContext(&blockCtx); err != nil {
		// Log the error but continue with default values
		fmt.Printf("Warning: Using default block context: %v\n", err)
	}

	// Rest of the function remains the same...
	txCtx := vm.TxContext{
		Origin:   caller,
		GasPrice: big.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, txCtx, state, e.ChainConfig, e.VMConfig)

	// Create contract
	// contractAddr := crypto.CreateAddress(caller, state.GetNonce(caller))
	state.SetNonce(caller, state.GetNonce(caller)+1)

	// Initialize memory and stack for execution
	contractRef := vm.AccountRef(caller)

	// Execute the deployment code
	ret, contractAddr, leftOverGas, err := evm.Create(contractRef, code, gasLimit, value256)

	// Check for gas overflow before calculating gasUsed
	if leftOverGas > gasLimit {
		return &ExecutionResult{
			ReturnData:   ret,
			GasUsed:      0,
			Error:        fmt.Errorf("gas uint64 overflow: leftOverGas=%d exceeds gasLimit=%d", leftOverGas, gasLimit),
			ContractAddr: contractAddr,
		}, fmt.Errorf("gas uint64 overflow: leftOverGas=%d exceeds gasLimit=%d", leftOverGas, gasLimit)
	}

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
	blockCtx := vm.BlockContext{
		CanTransfer: canTransferFn,
		Transfer:    transferFn,
		GetHash:     GetHashFn,
		Coinbase:    common.Address{},
		BlockNumber: new(big.Int).SetUint64(1),
		Time:        uint64(time.Now().UTC().Unix()),
		Difficulty:  big.NewInt(0),
		GasLimit:    gasLimit,
		BaseFee:     big.NewInt(0),
	}
	// Try to update with real blockchain info
	if err := UpdateBlockContext(&blockCtx); err != nil {
		// Log the error but continue with default values
		fmt.Printf("Warning: Using default block context: %v\n", err)
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

// CanTransfer checks if the account has enough balance to transfer the specified amount
func CanTransfer(db vm.StateDB, addr common.Address, amount *big.Int) bool {
	balance := db.GetBalance(addr)

	uintAmount, overflow := uint256.FromBig(amount)
	if overflow {
		return false
	}
	return balance.Cmp(uintAmount) >= 0
}

func Transfer(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {
	amount256, overflow := helper.ConvertBigToUint256(amount)
	if !overflow {
		db.SubBalance(sender, amount256, tracing.BalanceChangeTransfer)
		db.AddBalance(recipient, amount256, tracing.BalanceChangeTransfer)
	} else {
		panic("Overflow occurred during transfer")
	}
}
