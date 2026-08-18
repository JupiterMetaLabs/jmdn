package evm

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"

	"gossipnode/helper"
)

// Deterministic execution entry (audit EVM-02).
//
// DeployContract / ExecuteContract build the EVM block context from
// time.Now() and an HTTP GET (UpdateBlockContext -> /api/latest-block, GetHashFn
// -> /api/block/<n>). That is fine for the loopback debug/RPC surface but is
// FATAL inside a consensus apply: two nodes would see different time / block
// numbers / block hashes and compute different state. The *WithContext variants
// below take every context value explicitly from the agreed block and perform
// NO wall-clock or network I/O, so all nodes execute identically.
//
// These are the methods the execbridge EVM adapter (block-apply path) calls.
// The HTTP-based methods are left untouched for the existing loopback callers.

// DetBlockContext carries the deterministic, block-derived EVM context. Every
// field comes from the agreed block; GetHash resolves historical block hashes
// deterministically (supply a function over already-stored blocks; nil yields a
// zero hash for every height, which is deterministic across the fleet).
type DetBlockContext struct {
	BlockNumber uint64
	Time        int64 // block timestamp (unix seconds)
	Coinbase    common.Address
	GasLimit    uint64
	BaseFee     *big.Int                 // nil -> 0
	GetHash     func(uint64) common.Hash // nil -> zero hash (deterministic)

	// Random is the block's PREVRANDAO (audit EVM-29). It MUST be threaded into
	// vm.BlockContext.Random as a NON-NIL pointer, or go-ethereum treats the run
	// as pre-merge and never activates Shanghai even though NewChainConfig sets
	// ShanghaiTime=0 (IsShanghai is gated on isMerge == Random != nil). Set it to
	// a block-derived hash; the zero value is acceptable and deterministic, but a
	// real per-block value (e.g. the block hash) is preferred.
	Random common.Hash
}

// randomPtr returns a non-nil *common.Hash for vm.BlockContext.Random so the EVM
// runs post-merge (Shanghai) fork rules. Copies the value so the pointer is
// independent of the caller's struct.
func (b DetBlockContext) randomPtr() *common.Hash {
	r := b.Random
	return &r
}

func (b DetBlockContext) getHashFn() vm.GetHashFunc {
	if b.GetHash != nil {
		return b.GetHash
	}
	return func(uint64) common.Hash { return common.Hash{} }
}

func (b DetBlockContext) baseFee() *big.Int {
	if b.BaseFee != nil {
		return new(big.Int).Set(b.BaseFee)
	}
	return big.NewInt(0)
}

// DeployContractWithContext is DeployContract with a caller-supplied, fully
// deterministic block context (no time.Now(), no UpdateBlockContext HTTP).
func (e *EVMExecutor) DeployContractWithContext(state vm.StateDB, bctx DetBlockContext, caller common.Address, code []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
	value256, overflow := helper.ConvertBigToUint256(value)
	if overflow {
		return nil, fmt.Errorf("overflow during value conversion")
	}

	blockCtx := vm.BlockContext{
		CanTransfer: canTransferFn,
		Transfer:    transferFn,
		GetHash:     bctx.getHashFn(),
		Coinbase:    bctx.Coinbase,
		BlockNumber: new(big.Int).SetUint64(bctx.BlockNumber),
		Time:        uint64(bctx.Time),
		Difficulty:  big.NewInt(0),
		GasLimit:    bctx.GasLimit,
		BaseFee:     bctx.baseFee(),
		Random:      bctx.randomPtr(), // EVM-29: non-nil -> post-merge -> Shanghai
	}

	txCtx := vm.TxContext{Origin: caller, GasPrice: uint256.NewInt(0)}
	evmInstance := vm.NewEVM(blockCtx, state, e.ChainConfig, e.VMConfig)
	evmInstance.SetTxContext(txCtx)

	// Address derived from crypto.CreateAddress(caller, nonce-before-Create),
	// then nonce incremented after — same contract as DeployContract.
	ret, contractAddr, leftOverGas, err := evmInstance.Create(caller, code, gasLimit, value256)
	state.SetNonce(caller, state.GetNonce(caller)+1, tracing.NonceChangeReason(0))

	if leftOverGas > gasLimit {
		gerr := fmt.Errorf("gas uint64 overflow: leftOverGas=%d exceeds gasLimit=%d", leftOverGas, gasLimit)
		return &ExecutionResult{ReturnData: ret, GasUsed: 0, Error: gerr, ContractAddr: contractAddr}, gerr
	}
	return &ExecutionResult{
		ReturnData:   ret,
		GasUsed:      gasLimit - leftOverGas,
		Error:        err,
		ContractAddr: contractAddr,
	}, err
}

// ExecuteContractWithContext is ExecuteContract with a caller-supplied, fully
// deterministic block context.
func (e *EVMExecutor) ExecuteContractWithContext(state vm.StateDB, bctx DetBlockContext, caller, contractAddr common.Address, input []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
	value256, overflow := helper.ConvertBigToUint256(value)
	if overflow {
		return nil, fmt.Errorf("overflow during value conversion")
	}

	blockCtx := vm.BlockContext{
		CanTransfer: canTransferFn,
		Transfer:    transferFn,
		GetHash:     bctx.getHashFn(),
		Coinbase:    bctx.Coinbase,
		BlockNumber: new(big.Int).SetUint64(bctx.BlockNumber),
		Time:        uint64(bctx.Time),
		Difficulty:  big.NewInt(0),
		GasLimit:    bctx.GasLimit,
		BaseFee:     bctx.baseFee(),
		Random:      bctx.randomPtr(), // EVM-29: non-nil -> post-merge -> Shanghai
	}

	txCtx := vm.TxContext{Origin: caller, GasPrice: uint256.NewInt(0)}
	evmInstance := vm.NewEVM(blockCtx, state, e.ChainConfig, e.VMConfig)
	evmInstance.SetTxContext(txCtx)

	ret, leftOverGas, err := evmInstance.Call(caller, contractAddr, input, gasLimit, value256)

	// Overflow guard (the HTTP-based ExecuteContract omits this — EVM-30 note).
	if leftOverGas > gasLimit {
		gerr := fmt.Errorf("gas uint64 overflow: leftOverGas=%d exceeds gasLimit=%d", leftOverGas, gasLimit)
		return &ExecutionResult{ReturnData: ret, GasUsed: 0, Error: gerr}, gerr
	}
	return &ExecutionResult{
		ReturnData: ret,
		GasUsed:    gasLimit - leftOverGas,
		Error:      err,
	}, err
}
