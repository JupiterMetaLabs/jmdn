package SmartContract

import (
    "encoding/json"
    "fmt"
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
    
    // Run solc compiler
    cmd := exec.Command("solc", 
        "--combined-json", "abi,bin,bin-runtime",
        "--optimize", "--optimize-runs", "200",
        sourcePath)
    
    output, err := cmd.CombinedOutput()
    if err != nil {
        return nil, fmt.Errorf("solc compilation failed: %s - %w", output, err)
    }
    
    // Parse the JSON output
    var result struct {
        Contracts map[string]struct {
            Bin        string `json:"bin"`
            BinRuntime string `json:"bin-runtime"`
            ABI        string `json:"abi"`
        } `json:"contracts"`
        Version string `json:"version"`
    }
    
    if err := json.Unmarshal(output, &result); err != nil {
        return nil, fmt.Errorf("failed to parse solc output: %w", err)
    }
    
    // Convert to our format
    contracts := make(map[string]*CompiledContract)
    for path, contract := range result.Contracts {
        parts := strings.Split(path, ":")
        contractName := parts[len(parts)-1]
        
        contracts[contractName] = &CompiledContract{
            Bytecode:        "0x" + contract.Bin,
            ABI:             contract.ABI,
            DeployedBytecode: "0x" + contract.BinRuntime,
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