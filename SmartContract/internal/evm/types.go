package evm

import (
	"github.com/ethereum/go-ethereum/common"
)

// ExecutionResult holds the result of an EVM execution/deployment
type ExecutionResult struct {
	ReturnData   []byte
	GasUsed      uint64
	Error        error
	ContractAddr common.Address // Only populated for deployments
}
