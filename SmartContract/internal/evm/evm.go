package evm

import (
	"context"
	"fmt"
	"gossipnode/helper"
	"math/big"
	"time"

	"github.com/JupiterMetaLabs/ion"
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
		if l := evmLogger(); l != nil {
			l.Warn(context.Background(), "UpdateBlockContext failed, using defaults", ion.String("err", err.Error()))
		}
	}

	// Rest of the function remains the same...
	txCtx := vm.TxContext{
		Origin:   caller,
		GasPrice: uint256.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, state, e.ChainConfig, e.VMConfig)
	evm.SetTxContext(txCtx)

	// Execute the deployment code.
	// NOTE: The contract address is derived from crypto.CreateAddress(caller, current_nonce)
	// BEFORE the nonce is incremented.  We increment AFTER Create so that all callers
	// (deploy_contract.go, handlers.go, etc.) can predict the deployed address using the
	// nonce they read before calling DeployContract — no off-by-one adjustments needed.
	ret, contractAddr, leftOverGas, err := evm.Create(caller, code, gasLimit, value256)

	// Increment the caller's nonce now that the deployment has been attempted
	// (mirrors standard Ethereum: nonce counts committed transactions, successful or not).
	state.SetNonce(caller, state.GetNonce(caller)+1, tracing.NonceChangeReason(0))

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
		if l := evmLogger(); l != nil {
			l.Warn(context.Background(), "UpdateBlockContext failed, using defaults", ion.String("err", err.Error()))
		}
	}

	txCtx := vm.TxContext{
		Origin:   caller,
		GasPrice: uint256.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, state, e.ChainConfig, e.VMConfig)
	evm.SetTxContext(txCtx)

	// Call the contract
	ret, leftOverGas, err := evm.Call(caller, contractAddr, input, gasLimit, value256)

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

// Transfer moves amount from sender to recipient.
// Returns an error if the amount overflows uint256 — callers should handle this
// rather than the process crashing.
func Transfer(db vm.StateDB, sender, recipient common.Address, amount *big.Int) error {
	amount256, overflow := helper.ConvertBigToUint256(amount)
	if overflow {
		return fmt.Errorf("transfer amount overflow: sender=%s amount=%s", sender.Hex(), amount.String())
	}
	db.SubBalance(sender, amount256, tracing.BalanceChangeTransfer)
	db.AddBalance(recipient, amount256, tracing.BalanceChangeTransfer)
	return nil
}
