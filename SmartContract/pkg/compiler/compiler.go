package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/accounts/abi"
)

// CompiledContract holds compilation results
type CompiledContract struct {
	Bytecode         string   `json:"bytecode"`
	ABI              string   `json:"abi"`
	DeployedBytecode string   `json:"deployed_bytecode"`
	Name             string   `json:"name"`
	Path             string   `json:"path"`
	Errors           []string `json:"errors,omitempty"`
}

// CompileSolidity compiles Solidity source files
func CompileSolidity(sourcePath string) (map[string]*CompiledContract, error) {
	logger().Info(context.Background(), "Compiling Solidity contract",
		ion.String("source", sourcePath))

	// Make sure the artifacts directory exists
	artifactsDir := "./SmartContract/artifacts"
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create artifacts directory: %w", err)
	}

	// Read the source code
	sourceCode, err := ioutil.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}

	// Create a standard JSON input.
	//
	// CodeQL go/unsafe-quoting (alert #17): this used to be a fmt.Sprintf
	// template with sourceFileName spliced into a hand-written "%s" — a
	// filename containing a double quote (sourceFileName is
	// filepath.Base(sourcePath), which callers pass through from
	// user-supplied contract source paths) would break out of the JSON
	// string and inject arbitrary keys into the standard-json input handed
	// to solc. Build the input as a struct and let encoding/json do the
	// escaping instead of hand-constructing quoted JSON.
	sourceFileName := filepath.Base(sourcePath)
	type solcSource struct {
		Content string `json:"content"`
	}
	type solcSettings struct {
		OutputSelection map[string]map[string][]string `json:"outputSelection"`
		Optimizer       struct {
			Enabled bool `json:"enabled"`
			Runs    int  `json:"runs"`
		} `json:"optimizer"`
		EVMVersion string `json:"evmVersion"`
	}
	type solcStandardInput struct {
		Language string                `json:"language"`
		Sources  map[string]solcSource `json:"sources"`
		Settings solcSettings          `json:"settings"`
	}

	input := solcStandardInput{
		Language: "Solidity",
		Sources: map[string]solcSource{
			sourceFileName: {Content: string(sourceCode)},
		},
	}
	input.Settings.OutputSelection = map[string]map[string][]string{
		"*": {"*": {"abi", "evm.bytecode", "evm.deployedBytecode"}},
	}
	input.Settings.Optimizer.Enabled = true
	input.Settings.Optimizer.Runs = 200
	input.Settings.EVMVersion = "shanghai"

	standardJSONInputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal solc standard-json input: %w", err)
	}
	standardJSONInput := string(standardJSONInputBytes)

	// Create a temporary file for the JSON input
	inputFile, err := ioutil.TempFile("", "solc-input-*.json")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(inputFile.Name())

	if _, err := inputFile.Write([]byte(standardJSONInput)); err != nil {
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Run solc compiler with standard JSON input
	cmd := exec.Command("solc", "--standard-json", inputFile.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger().Error(context.Background(), "Solc execution failed", err,
			ion.String("output", string(output)))
		return nil, fmt.Errorf("solc compilation failed: %s - %w", output, err)
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

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse solc output: %w", err)
	}

	// Check for errors
	if len(result.Errors) > 0 {
		var messages []string
		for _, err := range result.Errors {
			messages = append(messages, err.Message)
		}
		return nil, fmt.Errorf("compilation errors: %s", strings.Join(messages, "; "))
	}

	// Convert to our format
	contracts := make(map[string]*CompiledContract)
	for _, fileContracts := range result.Contracts {
		for contractName, contract := range fileContracts {
			abiJSON, err := json.Marshal(contract.ABI)
			if err != nil {
				continue
			}

			contracts[contractName] = &CompiledContract{
				Bytecode:         "0x" + contract.EVM.Bytecode.Object,
				ABI:              string(abiJSON),
				DeployedBytecode: "0x" + contract.EVM.DeployedBytecode.Object,
				Name:             contractName,
				Path:             sourcePath,
			}

			// Save artifact to disk
			artifactPath := filepath.Join(artifactsDir, contractName+".json")
			artifactData, _ := json.MarshalIndent(contracts[contractName], "", "  ")
			if err := ioutil.WriteFile(artifactPath, artifactData, 0644); err != nil {
				logger().Error(context.Background(), "Failed to write contract artifact", err,
					ion.String("path", artifactPath))
			}
		}
	}

	return contracts, nil
}

// ParseABI parses the ABI JSON string into a structured ABI
func ParseABI(abiJSON string) (*abi.ABI, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %w", err)
	}
	return &parsedABI, nil
}
