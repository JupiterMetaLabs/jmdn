package evm

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/logger"
	"github.com/holiman/uint256"
)

// TraceResult is the JSON-RPC payload returned by debug_traceTransaction.
// It is a thin wrapper around the StructLogger's ExecutionResult JSON so that
// the Service layer does not need to import the go-ethereum tracer package.
type TraceResult struct {
	// Raw is the full JSON produced by StructLogger.GetResult().
	// Embedded as json.RawMessage so it is forwarded verbatim to the caller.
	json.RawMessage
}

// MarshalJSON satisfies json.Marshaler — forward the inner payload directly.
func (t *TraceResult) MarshalJSON() ([]byte, error) {
	if t == nil || t.RawMessage == nil {
		return []byte("null"), nil
	}
	return t.RawMessage, nil
}

// TraceTransaction re-executes a call against the supplied (pre-execution) stateDB
// and returns a structured opcode trace.
//
// NOTE: Historical pre-state reconstruction is a Phase-5 item.  Callers SHOULD
// pass the current StateDB as a best-effort approximation.  The gas and return
// values will be accurate for read-only calls; storage-mutating traces may
// differ from the original execution if the state has changed since the tx.
//
// Parameters:
//   - stateDB  – A vm.StateDB positioned at (or near) the pre-execution state.
//   - blockCtx – The BlockContext of the block that included the transaction.
//   - from     – The sender address.
//   - to       – The contract address (nil for contract creation).
//   - input    – ABI-encoded call data or constructor bytecode.
//   - value    – Value transferred (nil treated as zero).
//   - gasLimit – Gas limit for the re-execution.
//   - chainID  – JMDT chain ID (used to construct the ChainConfig).
func TraceTransaction(
	stateDB vm.StateDB,
	from common.Address,
	to *common.Address,
	input []byte,
	value *big.Int,
	gasLimit uint64,
	chainID int,
) (*TraceResult, error) {
	// Build the StructLogger tracer
	tracer := logger.NewStructLogger(&logger.Config{
		EnableMemory:     true,
		EnableReturnData: true,
	})

	// Build BlockContext (defaults mirroring evm.go)
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
	// Try to refresh with real chain state
	if err := UpdateBlockContext(&blockCtx); err != nil {
		fmt.Printf("[tracer] using default block context: %v\n", err)
	}

	// TxContext
	txCtx := vm.TxContext{
		Origin:   from,
		GasPrice: uint256.NewInt(0),
	}

	// Chain config (same as NewEVMExecutor)
	chainConfig := NewChainConfig(chainID)

	// Attach tracer hooks to vm.Config
	vmCfg := vm.Config{
		Tracer:    tracer.Hooks(),
		NoBaseFee: true,
		ExtraEips: []int{3855},
	}

	evmInstance := vm.NewEVM(blockCtx, stateDB, chainConfig, vmCfg)
	evmInstance.SetTxContext(txCtx)

	// Convert value
	var val256 *uint256.Int
	if value != nil && value.Sign() > 0 {
		var overflow bool
		val256, overflow = uint256.FromBig(value)
		if overflow {
			return nil, fmt.Errorf("TraceTransaction: value overflow")
		}
	} else {
		val256 = uint256.NewInt(0)
	}

	// Execute
	if to == nil {
		// Contract creation
		_, _, _, _ = evmInstance.Create(from, input, gasLimit, val256)
	} else {
		// Contract call
		_, _, _ = evmInstance.Call(from, *to, input, gasLimit, val256)
	}

	// Collect result from the tracer
	raw, err := tracer.GetResult()
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: tracer.GetResult: %w", err)
	}

	// Wrap in a TraceResult (verbatim JSON forwarded to the JSON-RPC caller)
	return &TraceResult{RawMessage: raw}, nil
}
