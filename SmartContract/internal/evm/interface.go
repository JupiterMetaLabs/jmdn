package evm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

// Executor defines the interface for executing smart contracts
// This allows for future alternative execution engines (e.g. Wasm) or mocking
type Executor interface {
	// DeployContract deploys a new contract
	DeployContract(state vm.StateDB, caller common.Address, code []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error)

	// ExecuteContract executes a function on an existing contract
	ExecuteContract(state vm.StateDB, caller common.Address, contractAddr common.Address, input []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error)
}
