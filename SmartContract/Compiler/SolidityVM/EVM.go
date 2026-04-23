package SolidityVM

import (
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/config/Types"
	"gossipnode/config/Types/Solidity"

	solc "github.com/imxyb/solc-go"
)

type EVM struct {
	compiledContract *Types.CompiledContract
}

func NewEVM(compiledContract *Types.CompiledContract) *EVM {
	return &EVM{compiledContract: Types.NewCompiledContract(compiledContract)}
}

// Compile compiles Solidity source code and returns a CompiledContract.
// This is a stateless operation - no file I/O, uses in-memory compilation via solc-go library.
func (e *EVM) Compile(sourceCode string) (*Types.CompiledContract, error) {
	// Use solc-go for in-memory compilation (no temp files needed)
	compiler, err := solc.GetCompiler("0.8.28")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize solc compiler: %w", err)
	}

	// Create Input struct
	input := &solc.Input{
		Language: "Solidity",
		Sources: map[string]solc.SourceIn{
			"contract.sol": {
				Content: sourceCode,
			},
		},
		Settings: solc.Settings{
			OutputSelection: map[string]map[string][]string{
				"*": {
					"*": {"abi", "evm.bytecode", "evm.deployedBytecode"},
				},
			},
			Optimizer: solc.Optimizer{
				Enabled: true,
				Runs:    200,
			},
			EVMVersion: "london",
		},
	}

	// Compile
	output, err := compiler.Compile(input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", Solidity.ErrCompilationFailed, err)
	}

	// Parse the JSON output
	var result struct {
		Contracts map[string]map[string]struct {
			ABI interface{} `json:"abi"`
			EVM struct {
				Bytecode struct {
					Object string `json:"object"`
				} `json:"bytecode"`
				DeployedBytecode struct {
					Object string `json:"object"`
				} `json:"deployedBytecode"`
			} `json:"evm"`
		} `json:"contracts"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	// Convert Output to JSON bytes
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, Solidity.ErrMarshalOutput
	}

	if err := json.Unmarshal(outputJSON, &result); err != nil {
		return nil, Solidity.ErrUnmarshalOutput
	}

	// Check for compilation errors
	if len(result.Errors) > 0 {
		var messages []string
		for _, err := range result.Errors {
			messages = append(messages, err.Message)
		}
		return nil, fmt.Errorf("%w: %s", Solidity.ErrCompilationFailed, strings.Join(messages, "; "))
	}

	// Extract the first contract from compilation results
	var contractData struct {
		ABI interface{} `json:"abi"`
		EVM struct {
			Bytecode struct {
				Object string `json:"object"`
			} `json:"bytecode"`
			DeployedBytecode struct {
				Object string `json:"object"`
			} `json:"deployedBytecode"`
		} `json:"evm"`
	}

	found := false
	for _, fileContracts := range result.Contracts {
		for _, contract := range fileContracts {
			contractData = contract
			found = true
			break
		}
		if found {
			break
		}
	}

	if !found {
		return nil, fmt.Errorf("%w: no contracts found in compilation output", Solidity.ErrCompilationFailed)
	}

	// Marshal ABI to JSON string
	abiJSON, err := json.Marshal(contractData.ABI)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal ABI", Solidity.ErrInvalidABI)
	}

	// Build and return CompiledContract using builder pattern
	compiledContract := Types.NewCompiledContract(nil).
		SetBytecode("0x" + contractData.EVM.Bytecode.Object).
		SetABI(string(abiJSON)).
		SetDeployedBytecode("0x" + contractData.EVM.DeployedBytecode.Object)

	// Return the value (not pointer) to match function signature
	return compiledContract, nil
}

// Run validates the compiled contract and returns execution metadata as JSON.
// This is a stateless operation that validates the contract is ready for execution
// and provides information about available functions and contract properties.
// To actually run/execute the contract and get output, you need an EVM runtime.
func (e *EVM) Run(contract *Types.CompiledContract) (string, error) {
	if contract == nil {
		return "", fmt.Errorf("contract cannot be nil")
	}

	// Validate contract has required fields
	if contract.GetBytecode() == "" {
		return "", fmt.Errorf("%w: bytecode is empty", Solidity.ErrInvalidBytecode)
	}
	if contract.GetDeployedBytecode() == "" {
		return "", fmt.Errorf("%w: deployed bytecode is empty", Solidity.ErrInvalidDeployedBytecode)
	}
	if contract.GetABI() == "" {
		return "", fmt.Errorf("%w: ABI is empty", Solidity.ErrInvalidABI)
	}

	// Parse ABI to extract function information
	var abiData []map[string]interface{}
	if err := json.Unmarshal([]byte(contract.GetABI()), &abiData); err != nil {
		return "", fmt.Errorf("%w: failed to parse ABI: %v", Solidity.ErrInvalidABI, err)
	}

	// Extract function information from ABI
	var functions []map[string]interface{}
	var events []map[string]interface{}

	for _, item := range abiData {
		itemType, ok := item["type"].(string)
		if !ok {
			continue
		}

		switch itemType {
		case "function":
			functions = append(functions, map[string]interface{}{
				"name":            item["name"],
				"inputs":          item["inputs"],
				"outputs":         item["outputs"],
				"stateMutability": item["stateMutability"],
			})
		case "event":
			events = append(events, map[string]interface{}{
				"name":   item["name"],
				"inputs": item["inputs"],
			})
		}
	}

	// Calculate bytecode sizes (remove 0x prefix for calculation)
	bytecodeSize := len(contract.GetBytecode())
	if strings.HasPrefix(contract.GetBytecode(), "0x") {
		bytecodeSize = (len(contract.GetBytecode()) - 2) / 2 // Each byte is 2 hex chars
	}

	deployedBytecodeSize := len(contract.GetDeployedBytecode())
	if strings.HasPrefix(contract.GetDeployedBytecode(), "0x") {
		deployedBytecodeSize = (len(contract.GetDeployedBytecode()) - 2) / 2
	}

	// Build execution metadata
	metadata := map[string]interface{}{
		"status":                 "ready",
		"bytecode_size":          bytecodeSize,
		"deployed_bytecode_size": deployedBytecodeSize,
		"functions_count":        len(functions),
		"events_count":           len(events),
		"functions":              functions,
		"events":                 events,
		"has_bytecode":           contract.GetBytecode() != "",
		"has_deployed_bytecode":  contract.GetDeployedBytecode() != "",
		"has_abi":                contract.GetABI() != "",
	}

	// Marshal to JSON string
	result, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal execution metadata: %w", err)
	}

	return string(result), nil
}
