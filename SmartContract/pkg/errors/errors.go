package errors

import (
	"errors"
	"fmt"
)

// Common error types for smart contract operations
var (
	// Address validation errors
	ErrInvalidAddress = errors.New("invalid address")
	ErrNilAddress     = errors.New("address cannot be nil")

	// Bytecode errors
	ErrInvalidBytecode = errors.New("invalid bytecode")
	ErrEmptyBytecode   = errors.New("bytecode cannot be empty")

	// Gas errors
	ErrInsufficientGas = errors.New("insufficient gas")
	ErrGasLimitTooLow  = errors.New("gas limit too low")

	// Contract errors
	ErrContractNotFound     = errors.New("contract not found")
	ErrContractExists       = errors.New("contract already exists")
	ErrContractDeployFailed = errors.New("contract deployment failed")

	// Compilation errors
	ErrCompilationFailed = errors.New("compilation failed")
	ErrInvalidSourceCode = errors.New("invalid source code")
	ErrCompilerNotFound  = errors.New("compiler not found")

	// Execution errors
	ErrExecutionReverted = errors.New("execution reverted")
	ErrExecutionFailed   = errors.New("execution failed")
	ErrOutOfGas          = errors.New("out of gas")

	// State errors
	ErrStateCommitFailed = errors.New("state commit failed")
	ErrStateNotFound     = errors.New("state not found")
	ErrInvalidState      = errors.New("invalid state")

	// Database errors
	ErrDatabaseConnection = errors.New("database connection failed")
	ErrDatabaseOperation  = errors.New("database operation failed")

	// Registry errors
	ErrRegistryNotFound = errors.New("registry entry not found")
	ErrRegistryFailed   = errors.New("registry operation failed")
)

// ContractError wraps an error with contract address context
type ContractError struct {
	Address string
	Err     error
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("contract %s: %v", e.Address, e.Err)
}

func (e *ContractError) Unwrap() error {
	return e.Err
}

// NewContractError creates a new contract error
func NewContractError(address string, err error) error {
	return &ContractError{
		Address: address,
		Err:     err,
	}
}

// CompilationError wraps compilation errors with source context
type CompilationError struct {
	Message string
	Line    int
	Column  int
}

func (e *CompilationError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("compilation error at line %d, column %d: %s", e.Line, e.Column, e.Message)
	}
	return fmt.Sprintf("compilation error: %s", e.Message)
}

// NewCompilationError creates a new compilation error
func NewCompilationError(message string, line, column int) error {
	return &CompilationError{
		Message: message,
		Line:    line,
		Column:  column,
	}
}

// ExecutionError wraps execution errors with gas and revert reason
type ExecutionError struct {
	Reason   string
	GasUsed  uint64
	Reverted bool
}

func (e *ExecutionError) Error() string {
	if e.Reverted {
		return fmt.Sprintf("execution reverted: %s (gas used: %d)", e.Reason, e.GasUsed)
	}
	return fmt.Sprintf("execution failed: %s (gas used: %d)", e.Reason, e.GasUsed)
}

// NewExecutionError creates a new execution error
func NewExecutionError(reason string, gasUsed uint64, reverted bool) error {
	return &ExecutionError{
		Reason:   reason,
		GasUsed:  gasUsed,
		Reverted: reverted,
	}
}
