package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// Limits on solc invocations. Compilation is reachable from the public
// JSON-RPC (solc_compile), and each call forks a solc process that can run
// for a long time on crafted input.
const (
	// MaxSourceBytes bounds the Solidity source accepted for one compile.
	MaxSourceBytes = 512 << 10
	// MaxConcurrentSolc bounds solc processes running at once.
	MaxConcurrentSolc = 2
	// SolcTimeout bounds a single solc run; the process is killed after it.
	SolcTimeout = 30 * time.Second
)

// ErrCompilerBusy is returned when every solc slot is in use.
var ErrCompilerBusy = errors.New("compiler busy: too many concurrent compilations, retry later")

var solcSlots = make(chan struct{}, MaxConcurrentSolc)

// solcCommand is the executable invoked; overridable in tests.
var solcCommand = "solc"

// runSolc runs solc on a standard-json input file under the concurrency and
// time limits. It fails fast with ErrCompilerBusy instead of queueing, so a
// burst of requests cannot pile up waiting goroutines.
func runSolc(inputPath string) ([]byte, error) {
	select {
	case solcSlots <- struct{}{}:
	default:
		return nil, ErrCompilerBusy
	}
	defer func() { <-solcSlots }()

	ctx, cancel := context.WithTimeout(context.Background(), SolcTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, solcCommand, "--standard-json", inputPath)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("solc timed out after %s", SolcTimeout)
	}
	return out, err
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
	sourceCode, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	if len(sourceCode) > MaxSourceBytes {
		return nil, fmt.Errorf("source too large: %d bytes (max %d)", len(sourceCode), MaxSourceBytes)
	}

	// Create a standard JSON input.
	//
	// CodeQL go/unsafe-quoting (alert #17): this used to be a fmt.Sprintf
	// template with sourceFileName spliced into a hand-written "%s" with no
	// escaping — a filename containing a double quote would break out of
	// the JSON string and inject arbitrary keys into the standard-json
	// input handed to solc. Not reachable today: CompileSolidity's only
	// caller (SmartContract/internal/router/handlers.go's CompileContract)
	// derives sourcePath from os.CreateTemp, not from any caller-supplied
	// name — sourceFileName can't carry a quote in practice right now.
	// Fixed anyway, by building the input as a struct and letting
	// encoding/json do the escaping instead of hand-constructing quoted
	// JSON: it removes the injection class outright rather than relying on
	// every future caller of this exported function continuing to only
	// pass a generated temp-file path.
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
	inputFile, err := os.CreateTemp("", "solc-input-*.json")
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
	output, err := runSolc(inputFile.Name())
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
			if err := os.WriteFile(artifactPath, artifactData, 0644); err != nil {
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
