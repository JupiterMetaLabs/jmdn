package Tests

import (
	"gossipnode/SmartContract/Compiler/Adapter"
	"testing"
)

const 	sourceCode = `
	// SPDX-License-Identifier: MIT
	pragma solidity ^0.8.0;
	contract MyContract {
		function myFunction() public pure returns (string memory) {
			return "Hello, World!";
		}
	}
`

func TestCompileContractSolidity(t *testing.T) {
	compiledContract, err := Adapter.NewSolidityCompiler().Compile(sourceCode)
	if err != nil {
		t.Fatalf("Failed to compile contract: %v", err)
	}
	t.Logf("Compiled contract: %v", compiledContract.GetBytecode())
	t.Logf("Compiled contract ABI: %v", compiledContract.GetABI())
	t.Logf("Compiled contract deployed bytecode: %v", compiledContract.GetDeployedBytecode())
}

func TestRunContractSolidity(t *testing.T) {
	compiledContract, err := Adapter.NewSolidityCompiler().Compile(sourceCode)
	if err != nil {
		t.Fatalf("Failed to compile contract: %v", err)
	}
	SetCompiledContract := Adapter.NewSolidityCompiler().SetCompiledContract(compiledContract.GetBytecode(), compiledContract.GetABI(), compiledContract.GetDeployedBytecode())
	if SetCompiledContract == nil {
		t.Fatalf("Failed to set compiled contract")
	}
	ExecutionMetadata, err := Adapter.NewSolidityCompiler().Run(SetCompiledContract)
	if err != nil {
		t.Fatalf("Failed to run contract: %v", err)
	}
	t.Logf("Execution metadata: %v", ExecutionMetadata)
}