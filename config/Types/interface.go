package Types

import "github.com/ethereum/go-ethereum/accounts/abi"


type Compiler interface {
	// stateless function to compile the source code to the bytecode
	Compile(sourceCode string) (*CompiledContract, error)

	// stateless function to set the compiled contract
	SetCompiledContract(bytecode string, abi string, deployedBytecode string) *CompiledContract

	// stateful function to run the compiled contract
	Run(contract *CompiledContract) (string, error)

	// stateless function to parse the ABI
	ParseABI(abiString string) (*abi.ABI, error)
}
