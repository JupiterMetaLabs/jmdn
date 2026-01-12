package router

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"gossipnode/SmartContract"
	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/rs/zerolog/log"
)

// SmartContractRouter orchestrates smart contract operations
type SmartContractRouter struct {
	executor *SmartContract.EVMExecutor
	stateDB  vm.StateDB
	chainID  int
}

// NewSmartContractRouter creates a new router instance
// stateDB can be InMemoryStateDB or ImmuStateDB depending on your needs
func NewSmartContractRouter(chainID int, stateDB vm.StateDB) (*SmartContractRouter, error) {
	log.Info().Int("chainID", chainID).Msg("Initializing SmartContract router")

	// Initialize EVM executor
	executor := SmartContract.NewEVMExecutor(chainID)

	return &SmartContractRouter{
		executor: executor,
		stateDB:  stateDB,
		chainID:  chainID,
	}, nil
}

// ============================================================================
// Compilation
// ============================================================================

// CompileContract compiles Solidity source code
func (r *SmartContractRouter) CompileContract(sourceCode string) (*SmartContract.CompiledContract, error) {
	log.Debug().Msg("Compiling Solidity contract")

	// Write source to temp file
	tmpFile, err := os.CreateTemp("", "contract-*.sol")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(sourceCode); err != nil {
		return nil, fmt.Errorf("failed to write source: %w", err)
	}
	tmpFile.Close()

	// Use SmartContract.CompileSolidity - NO broken dependencies!
	contracts, err := SmartContract.CompileSolidity(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("compilation failed: %w", err)
	}

	if len(contracts) == 0 {
		return nil, fmt.Errorf("no contracts found")
	}

	// Get first contract
	var contract *SmartContract.CompiledContract
	for _, c := range contracts {
		contract = c
		break
	}

	log.Info().
		Str("bytecode_size", fmt.Sprintf("%d bytes", len(contract.Bytecode))).
		Msg("Contract compiled successfully")

	return contract, nil
}

// ============================================================================
// Deployment
// ============================================================================

// DeployContract deploys a contract to the network
func (r *SmartContractRouter) DeployContract(req *proto.DeployContractRequest) (*proto.ExecutionResult, error) {
	log.Info().Hex("caller", req.Caller).Msg("Deploying contract")

	// Parse caller address
	caller := common.BytesToAddress(req.Caller)

	// Parse value
	value := new(big.Int).SetBytes(req.Value)

	// Decode bytecode
	bytecode, err := hexToBytes(req.Bytecode)
	if err != nil {
		return nil, fmt.Errorf("invalid bytecode: %w", err)
	}

	// If constructor args provided, append them
	if len(req.ConstructorArgs) > 0 {
		bytecode = append(bytecode, req.ConstructorArgs...)
	}

	// Deploy contract
	result, err := r.executor.DeployContract(r.stateDB, caller, bytecode, value, req.GasLimit)
	if err != nil {
		return &proto.ExecutionResult{
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	// Note: State changes are automatically persisted by the StateDB implementation
	// InMemoryStateDB keeps changes in RAM, ImmuStateDB commits to database

	log.Info().
		Hex("contract_address", result.ContractAddr.Bytes()).
		Uint64("gas_used", result.GasUsed).
		Msg("Contract deployed successfully")

	return &proto.ExecutionResult{
		ReturnData:      result.ReturnData,
		GasUsed:         result.GasUsed,
		ContractAddress: result.ContractAddr.Bytes(),
		Success:         result.Error == nil,
	}, nil
}

// ============================================================================
// Execution
// ============================================================================

// ExecuteContract executes a contract function (state-changing)
func (r *SmartContractRouter) ExecuteContract(req *proto.ExecuteContractRequest) (*proto.ExecutionResult, error) {
	log.Info().
		Hex("caller", req.Caller).
		Hex("contract", req.ContractAddress).
		Msg("Executing contract")

	// Parse addresses
	caller := common.BytesToAddress(req.Caller)
	contractAddr := common.BytesToAddress(req.ContractAddress)

	// Parse value
	value := new(big.Int).SetBytes(req.Value)

	// Execute contract
	result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, req.Input, value, req.GasLimit)
	if err != nil {
		return &proto.ExecutionResult{
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	// Note: State changes are automatically persisted by the StateDB implementation

	log.Info().
		Uint64("gas_used", result.GasUsed).
		Int("return_data_size", len(result.ReturnData)).
		Msg("Contract executed successfully")

	return &proto.ExecutionResult{
		ReturnData: result.ReturnData,
		GasUsed:    result.GasUsed,
		Success:    result.Error == nil,
	}, nil
}

// CallContract performs a read-only contract call
func (r *SmartContractRouter) CallContract(req *proto.CallContractRequest) ([]byte, error) {
	log.Debug().Hex("contract", req.ContractAddress).Msg("Calling contract (read-only)")

	// Parse addresses
	caller := common.BytesToAddress(req.Caller)
	contractAddr := common.BytesToAddress(req.ContractAddress)

	// Execute contract (read-only, no state changes)
	result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, req.Input, big.NewInt(0), 10000000)
	if err != nil {
		return nil, fmt.Errorf("call failed: %w", err)
	}

	if result.Error != nil {
		return nil, result.Error
	}

	return result.ReturnData, nil
}

// ============================================================================
// Contract Information
// ============================================================================

// GetContractCode retrieves contract code and metadata
func (r *SmartContractRouter) GetContractCode(contractAddress []byte) ([]byte, string, *proto.ContractMetadata, error) {
	addr := common.BytesToAddress(contractAddress)

	// Get contract code from state
	code := r.stateDB.GetCode(addr)
	if len(code) == 0 {
		return nil, "", nil, fmt.Errorf("contract not found at address %s", addr.Hex())
	}

	// TODO: Retrieve ABI and metadata from database/storage
	// For now, return empty ABI and minimal metadata
	metadata := &proto.ContractMetadata{
		Address: contractAddress,
	}

	return code, "", metadata, nil
}

// GetStorage retrieves a storage slot value
func (r *SmartContractRouter) GetStorage(contractAddress []byte, storageKey []byte) ([]byte, error) {
	addr := common.BytesToAddress(contractAddress)
	key := common.BytesToHash(storageKey)

	value := r.stateDB.GetState(addr, key)
	return value.Bytes(), nil
}

// ListContracts lists deployed contracts (stub implementation)
func (r *SmartContractRouter) ListContracts(fromBlock, toBlock uint64, limit uint32) ([]*proto.ContractMetadata, error) {
	// TODO: Implement contract registry with database queries
	log.Warn().Msg("ListContracts not fully implemented")
	return []*proto.ContractMetadata{}, nil
}

// ============================================================================
// Gas Estimation
// ============================================================================

// EstimateGas estimates gas for a transaction
func (r *SmartContractRouter) EstimateGas(req *proto.EstimateGasRequest) (uint64, error) {
	log.Debug().Msg("Estimating gas")

	caller := common.BytesToAddress(req.Caller)
	value := new(big.Int).SetBytes(req.Value)

	var gasUsed uint64

	if len(req.ContractAddress) == 0 {
		// Contract deployment
		bytecode, err := hexToBytes(string(req.Input))
		if err != nil {
			bytecode = req.Input
		}

		result, err := r.executor.DeployContract(r.stateDB, caller, bytecode, value, 10000000)
		if err != nil {
			return 0, fmt.Errorf("gas estimation failed: %w", err)
		}
		gasUsed = result.GasUsed
	} else {
		// Contract call
		contractAddr := common.BytesToAddress(req.ContractAddress)
		result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, req.Input, value, 10000000)
		if err != nil {
			return 0, fmt.Errorf("gas estimation failed: %w", err)
		}
		gasUsed = result.GasUsed
	}

	// Add 20% buffer for safety
	estimatedGas := gasUsed + (gasUsed / 5)

	log.Debug().Uint64("gas_used", gasUsed).Uint64("estimated_gas", estimatedGas).Msg("Gas estimated")

	return estimatedGas, nil
}

// ============================================================================
// ABI Utilities
// ============================================================================

// EncodeFunctionCall encodes a function call to ABI bytes
func (r *SmartContractRouter) EncodeFunctionCall(abiJSON string, functionName string, args [][]byte) ([]byte, error) {
	// Parse ABI
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %w", err)
	}

	// Convert args to interface{}
	var interfaceArgs []interface{}
	for _, arg := range args {
		interfaceArgs = append(interfaceArgs, arg)
	}

	// Pack function call
	packed, err := parsedABI.Pack(functionName, interfaceArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack function call: %w", err)
	}

	return packed, nil
}

// DecodeFunctionOutput decodes function output
func (r *SmartContractRouter) DecodeFunctionOutput(abiJSON string, functionName string, outputData []byte) ([][]byte, error) {
	// Parse ABI
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %w", err)
	}

	// Unpack output
	method, ok := parsedABI.Methods[functionName]
	if !ok {
		return nil, fmt.Errorf("function %s not found in ABI", functionName)
	}

	values, err := method.Outputs.Unpack(outputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack output: %w", err)
	}

	// Convert to [][]byte
	var result [][]byte
	for _, v := range values {
		// Marshal to JSON then to bytes for simplicity
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			continue
		}
		result = append(result, jsonBytes)
	}

	return result, nil
}

// ============================================================================
// Helper Functions
// ============================================================================

// hexToBytes converts hex string to bytes
func hexToBytes(hexStr string) ([]byte, error) {
	// Remove 0x prefix if present
	hexStr = strings.TrimPrefix(hexStr, "0x")

	// Decode hex
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex string: %w", err)
	}

	return decoded, nil
}
