/*
Compiler strategy implementation.

This follows the Strategy design pattern, enabling dynamic selection of different compiler or emulator implementations.
It promotes extensibility by allowing new compilation strategies to be added without modifying the compiler’s core logic.
*/
package Adapter

import (
	"gossipnode/SmartContract/Compiler/SolidityVM"
	"gossipnode/config/Types"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

type SolidityCompiler struct{}

func NewSolidityCompiler() Types.Compiler {
	return &SolidityCompiler{}
}

func (c *SolidityCompiler) Compile(sourceCode string) (*Types.CompiledContract, error) {
	evm := SolidityVM.NewEVM(nil)
	compiledContract, err := evm.Compile(sourceCode)
	if err != nil {
		return nil, err
	}
	return compiledContract, nil
}

func (c *SolidityCompiler) SetCompiledContract(bytecode string, abi string, deployedBytecode string) *Types.CompiledContract {
	return Types.NewCompiledContract(nil).
		SetBytecode(bytecode).
		SetABI(abi).
		SetDeployedBytecode(deployedBytecode)
}

func (c *SolidityCompiler) Run(contract *Types.CompiledContract) (string, error) {
	evm := SolidityVM.NewEVM(contract)
	return evm.Run(contract)
}

func (c *SolidityCompiler) ParseABI(abiString string) (*abi.ABI, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abiString))
	if err != nil {
		return nil, err
	}
	return &parsedABI, nil
}