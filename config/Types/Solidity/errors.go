package Solidity

import "errors"

var (
	ErrInvalidSourceCode       = errors.New("invalid source code")
	ErrCompilationFailed       = errors.New("compilation failed")
	ErrInvalidBytecode         = errors.New("invalid bytecode")
	ErrInvalidABI              = errors.New("invalid ABI")
	ErrInvalidDeployedBytecode = errors.New("invalid deployed bytecode")
	ErrInvalidCompiler         = errors.New("invalid compiler")
	ErrMarshalOutput           = errors.New("failed to marshal output")
	ErrUnmarshalOutput         = errors.New("failed to unmarshal output")
)
