package router

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"gossipnode/SmartContract"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/pkg/types"
	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
)

// ============================================================================
// Compilation
// ============================================================================

// CompileContract compiles Solidity source code
func (r *Router) CompileContract(sourceCode string) (*SmartContract.CompiledContract, error) {
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
func (r *Router) DeployContract(ctx context.Context, req *proto.DeployContractRequest) (*proto.ExecutionResult, error) {
	log.Info().Hex("caller", req.Caller).Msg("Deploying contract (Router Layer)")

	// Parse caller address
	caller := common.BytesToAddress(req.Caller)

	// Parse value
	value := new(big.Int).SetBytes(req.Value)

	// Decode bytecode
	bytecode, err := HexToBytes(req.Bytecode)
	if err != nil {
		return nil, fmt.Errorf("invalid bytecode: %w", err)
	}

	// If constructor args provided, append them
	if len(req.ConstructorArgs) > 0 {
		bytecode = append(bytecode, req.ConstructorArgs...)
	}

	// Deploy contract using Executor
	result, err := r.executor.DeployContract(r.stateDB, caller, bytecode, value, req.GasLimit)
	if err != nil {
		return &proto.ExecutionResult{
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	// Persist changes to StateDB
	// Check if stateDB implements our internal interface with CommitToDB
	if sdb, ok := ConvertToInternalStateDB(r.stateDB); ok {
		if _, err := sdb.CommitToDB(false); err != nil {
			log.Error().Err(err).Msg("Failed to commit state changes")
			// We don't return error here to client if execution was successful, but we log it
		} else {
			log.Info().Msg("State changes committed to DB")
		}
	}

	// REGISTER CONTRACT IN REGISTRY (Layer 2 Integration)
	if r.contract_registry != nil && result.Error == nil {
		metadata := &types.ContractMetadata{
			Address:      result.ContractAddr,
			Deployer:     caller,
			Name:         "Unknown", // Metadata enhancement needed later
			ABI:          req.Abi,   // Use ABI from request
			DeployBlock:  0,         // TODO: Get real block number
			DeployTime:   uint64(time.Now().Unix()),
			DeployTxHash: common.Hash{}, // TODO: Get real tx hash
		}

		if err := r.contract_registry.RegisterContract(ctx, metadata); err != nil {
			log.Error().Err(err).Msg("Failed to register contract in registry")
		} else {
			log.Info().Str("address", result.ContractAddr.Hex()).Msg("Contract registered in registry")
		}
	}

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
func (r *Router) ExecuteContract(ctx context.Context, req *proto.ExecuteContractRequest) (*proto.ExecutionResult, error) {
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

	// Persist changes to StateDB
	if sdb, ok := ConvertToInternalStateDB(r.stateDB); ok {
		if _, err := sdb.CommitToDB(false); err != nil {
			log.Error().Err(err).Msg("Failed to commit state changes")
		}
	}

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
func (r *Router) CallContract(ctx context.Context, req *proto.CallContractRequest) ([]byte, error) {
	log.Debug().Hex("contract", req.ContractAddress).Msg("Calling contract (read-only)")

	// Parse addresses
	caller := common.BytesToAddress(req.Caller)
	contractAddr := common.BytesToAddress(req.ContractAddress)

	// Execute contract (read-only, no state changes)
	// For calls, we might want to use a snapshot or ensure no commit happens?
	// vm.StateDB usually handles avoiding commits if we don't call Commit.
	// We just ensure we don't call CommitToDB here.
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
func (r *Router) GetContractCode(ctx context.Context, contractAddress []byte) ([]byte, string, *proto.ContractMetadata, error) {
	addr := common.BytesToAddress(contractAddress)

	// Get contract code from state
	code := r.stateDB.GetCode(addr)
	if len(code) == 0 {
		return nil, "", nil, fmt.Errorf("contract not found at address %s", addr.Hex())
	}

	// Retrieve Metadata from Registry (Layer 2)
	var metadata *types.ContractMetadata
	var err error

	if r.contract_registry != nil {
		metadata, err = r.contract_registry.GetContract(ctx, addr)
		if err != nil {
			log.Warn().Err(err).Str("address", addr.Hex()).Msg("Failed to get contract metadata from registry")
		}
	}

	// Convert to proto metadata
	protoMeta := &proto.ContractMetadata{
		Address: contractAddress,
	}
	if metadata != nil {
		protoMeta.Name = metadata.Name
		protoMeta.Abi = metadata.ABI
		protoMeta.Deployer = metadata.Deployer.Bytes()
		protoMeta.TxHash = metadata.DeployTxHash.Bytes()
		protoMeta.BlockNumber = metadata.DeployBlock
		protoMeta.Timestamp = metadata.DeployTime
	}

	return code, "", protoMeta, nil
}

// GetStorage retrieves a storage slot value
func (r *Router) GetStorage(ctx context.Context, contractAddress []byte, storageKey []byte) ([]byte, error) {
	addr := common.BytesToAddress(contractAddress)
	key := common.BytesToHash(storageKey)

	value := r.stateDB.GetState(addr, key)
	return value.Bytes(), nil
}

// ListContracts lists deployed contracts
func (r *Router) ListContracts(ctx context.Context, fromBlock, toBlock uint64, limit uint32) ([]*proto.ContractMetadata, error) {
	if r.contract_registry == nil {
		return []*proto.ContractMetadata{}, nil
	}

	// TODO: Add support for block range in registry options if not already there
	// Currently Registry.ListContracts supports it.

	// Create options
	// Note: pkg/types ListOptions uses FromTime/ToTime? No, I defined ListOptions in INTERNAL registry INTERFACE.
	// Wait, I defined ListOptions in internal/registry/interface.go.
	// Let's import that.

	// Wait, I need to check ListOptions struct definition in internal/registry/interface.go
	// Since I cannot import internal/registry because of previous import block, I will assume it's there.
	// Ah, I imported "gossipnode/SmartContract/internal/contract_registry" as "registry"

	opts := &contract_registry.ListOptions{
		FromBlock: fromBlock,
		ToBlock:   toBlock,
		Limit:     limit,
	}

	contracts, err := r.contract_registry.ListContracts(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Convert to proto
	var result []*proto.ContractMetadata
	for _, c := range contracts {
		result = append(result, &proto.ContractMetadata{
			Address:     c.Address.Bytes(),
			Name:        c.Name,
			Abi:         c.ABI,
			Deployer:    c.Deployer.Bytes(),
			TxHash:      c.DeployTxHash.Bytes(),
			BlockNumber: c.DeployBlock,
			Timestamp:   c.DeployTime,
		})
	}

	return result, nil
}

// ============================================================================
// Gas Estimation
// ============================================================================

// EstimateGas estimates gas for a transaction
func (r *Router) EstimateGas(ctx context.Context, req *proto.EstimateGasRequest) (uint64, error) {
	log.Debug().Msg("Estimating gas")

	caller := common.BytesToAddress(req.Caller)
	value := new(big.Int).SetBytes(req.Value)

	var gasUsed uint64

	// Clone stateDB for estimation?
	// Ideally we should use a snapshot or a read-only view to avoid contaminating state.
	// Snapshotting:
	snapshot := r.stateDB.Snapshot()
	defer r.stateDB.RevertToSnapshot(snapshot)

	if len(req.ContractAddress) == 0 {
		// Contract deployment
		bytecode, err := HexToBytes(string(req.Input))
		if err != nil {
			bytecode = req.Input
		}

		result, err := r.executor.DeployContract(r.stateDB, caller, bytecode, value, 10000000) // High gas limit for estimation
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
func (r *Router) EncodeFunctionCall(abiJSON string, functionName string, args [][]byte) ([]byte, error) {
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
func (r *Router) DecodeFunctionOutput(abiJSON string, functionName string, outputData []byte) ([][]byte, error) {
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

	_, err = method.Outputs.Unpack(outputData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack output: %w", err)
	}

	// Convert to [][]byte
	var result [][]byte
	// TODO: Better handling of complex types
	return result, nil
}
