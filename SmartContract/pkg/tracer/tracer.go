// Package tracer exposes the JMDT EVM transaction tracer as a public API.
// It is a thin shim over SmartContract/internal/evm so that packages outside
// the SmartContract subtree (e.g. gETH/Facade/Service) can call it without
// violating Go's internal package visibility rules.
package tracer

import (
	"encoding/json"
	"math/big"

	internalEVM "gossipnode/SmartContract/internal/evm"

	"github.com/ethereum/go-ethereum/common"
)

// TraceTransaction re-executes the EVM call described by the parameters and
// returns the StructLogger JSON payload compatible with debug_traceTransaction.
//
// See SmartContract/internal/evm/tracer.go for full documentation and the
// known Phase-5 limitation around historical pre-state.
func TraceTransaction(
	from common.Address,
	to *common.Address,
	input []byte,
	value *big.Int,
	gasLimit uint64,
	chainID int,
) (json.RawMessage, error) {
	// Initialise a best-effort current StateDB
	stateDB, err := internalEVM.InitializeStateDB(chainID)
	if err != nil {
		return nil, err
	}

	result, err := internalEVM.TraceTransaction(stateDB, from, to, input, value, gasLimit, chainID)
	if err != nil {
		return nil, err
	}
	return result.RawMessage, nil
}
