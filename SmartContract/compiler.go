package SmartContract

import (
	"encoding/json"
	"fmt"
	"gossipnode/helper"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/rs/zerolog/log"
)

// CompiledContract holds compilation results
type CompiledContract struct {
    Bytecode       string            `json:"bytecode"`
    ABI            string            `json:"abi"`
    DeployedBytecode string          `json:"deployed_bytecode"`
    Name           string            `json:"name"`
    Path           string            `json:"path"`
    Errors         []string          `json:"errors,omitempty"`
}

// CompileSolidity compiles Solidity source files
func CompileSolidity(sourcePath string) (map[string]*CompiledContract, error) {
    log.Info().Str("source", sourcePath).Msg("Compiling Solidity contract")
    
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
    
    // Create a standard JSON input
    sourceFileName := filepath.Base(sourcePath)
    standardJSONInput := fmt.Sprintf(`{
        "language": "Solidity",
        "sources": {
            "%s": {
                "content": %s
            }
        },
        "settings": {
            "outputSelection": {
                "*": {
                    "*": ["abi", "evm.bytecode", "evm.deployedBytecode"]
                }
            },
            "optimizer": {
                "enabled": true,
                "runs": 200
            }
        }
    }`, sourceFileName, string(helper.ToJSON(string(sourceCode))))
    
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
                Bytecode:        "0x" + contract.EVM.Bytecode.Object,
                ABI:             string(abiJSON),
                DeployedBytecode: "0x" + contract.EVM.DeployedBytecode.Object,
                Name:            contractName,
                Path:            sourcePath,
            }
            
            // Save artifact to disk
            artifactPath := filepath.Join(artifactsDir, contractName+".json")
            artifactData, _ := json.MarshalIndent(contracts[contractName], "", "  ")
            if err := ioutil.WriteFile(artifactPath, artifactData, 0644); err != nil {
                log.Error().Err(err).Str("path", artifactPath).Msg("Failed to write contract artifact")
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