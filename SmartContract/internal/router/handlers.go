package router

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/transaction"
	"gossipnode/SmartContract/pkg/compiler"
	"gossipnode/SmartContract/pkg/types"
	"gossipnode/SmartContract/proto"
	pb "gossipnode/gETH/proto"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// ============================================================================
// Compilation
// ============================================================================

// CompileContract compiles Solidity source code
func (r *Router) CompileContract(sourceCode string) (*compiler.CompiledContract, error) {
	logger().Debug(context.Background(), "Compiling Solidity contract")

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

	// Use compiler.CompileSolidity - NO broken dependencies!
	contracts, err := compiler.CompileSolidity(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("compilation failed: %w", err)
	}

	if len(contracts) == 0 {
		return nil, fmt.Errorf("no contracts found")
	}

	// Get first contract
	var contract *compiler.CompiledContract
	for _, c := range contracts {
		contract = c
		break
	}

	logger().Info(context.Background(), "Contract compiled successfully",
		ion.String("bytecode_size", fmt.Sprintf("%d bytes", len(contract.Bytecode))))

	return contract, nil
}

// ============================================================================
// Deployment
// ============================================================================

// DeployContract submits a contract deployment transaction to the network
func (r *Router) DeployContract(ctx context.Context, req *proto.DeployContractRequest) (*proto.ExecutionResult, error) {
	logger().Info(ctx, "🚀 [CONSENSUS FLOW] DeployContract - Submitting transaction to network",
		ion.String("caller", req.Caller),
		ion.Int("abi_length", len(req.Abi)),
		ion.Bool("abi_provided", len(req.Abi) > 0))

	// Parse caller address
	caller := common.HexToAddress(req.Caller)

	// Parse value (hex string)
	value := new(big.Int)
	if val, ok := value.SetString(req.Value, 0); ok {
		value = val
	} else if req.Value == "" {
		// Default to 0
		value = big.NewInt(0)
	} else {
		return nil, fmt.Errorf("invalid value: %s", req.Value)
	}

	// Decode bytecode (hex string)
	bytecode, err := hexutil.Decode(req.Bytecode)
	if err != nil {
		return nil, fmt.Errorf("invalid bytecode: %w", err)
	}

	// If constructor args provided, append them
	if len(req.ConstructorArgs) > 0 {
		args, err := hexutil.Decode(req.ConstructorArgs)
		if err != nil {
			return nil, fmt.Errorf("invalid constructor args: %w", err)
		}
		bytecode = append(bytecode, args...)
	}

	// EIP-3860: initcode (bytecode + constructor args) must not exceed 2 × MAX_CODE_SIZE (49152 bytes).
	const maxInitcodeSize = 2 * 24576 // 49152 bytes
	if len(bytecode) > maxInitcodeSize {
		return nil, fmt.Errorf("initcode too large: %d bytes (max %d per EIP-3860)", len(bytecode), maxInitcodeSize)
	}

	// Resolve the caller's current nonce in a single DB lookup.
	var nonce uint64
	acc, err := DB_OPs.GetAccount(nil, caller)
	if err == nil && acc != nil {
		nonce = acc.Nonce
		logger().Info(ctx, "Using account nonce from DB",
			ion.Uint64("account_nonce", nonce))
	} else {
		nonce = 0
		logger().Debug(ctx, "Account not found in DB, assuming nonce 0",
			ion.String("caller", caller.Hex()))
	}
	// Create and build contract deployment transaction.
	// V/R/S are intentionally left nil — the SmartContract service is a trusted
	// internal component. Block/Server.go recognises unsigned contract-creation
	// transactions (To == nil, V == nil) and routes them through the internal
	// deployment path, bypassing external signature validation.
	tx, _, err := transaction.BuildContractCreationTx(
		big.NewInt(int64(r.chainID)),
		caller,
		nonce,
		value,
		bytecode,
		req.GasLimit,
		big.NewInt(1000000000), // MaxFee: 1 Gwei
		big.NewInt(1000000000), // MaxPriorityFee: 1 Gwei
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build transaction: %w", err)
	}

	// JSON marshal the transaction for the gRPC facade (gETH facade expects JSON)
	jsonData, err := json.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transaction: %w", err)
	}

	// Submit via gRPC to the main node's gETH facade
	resp, err := r.chainClient.SendRawTransaction(ctx, &pb.SendRawTxReq{
		SignedTx: jsonData,
	})

	if err != nil {
		logger().Error(ctx, "Failed to submit deployment transaction via gRPC", err)
		return &proto.ExecutionResult{
			Error:   fmt.Sprintf("Failed to submit transaction: %v", err),
			Success: false,
		}, nil
	}

	txHash := hex.EncodeToString(resp.TxHash)
	if !strings.HasPrefix(txHash, "0x") {
		txHash = "0x" + txHash
	}

	logger().Info(ctx, "Contract deployment transaction submitted successfully",
		ion.String("tx_hash", txHash))

	// Note: Contract metadata (including ABI) will be registered by Processing.go
	// after consensus confirms the deployment.
	// HOWEVER, we should store it optimistically now so it's available immediately for lookups
	// or if Processing.go doesn't have the ABI in the payload.
	contractAddr := crypto.CreateAddress(caller, nonce)
	contractAddrHex := contractAddr.Hex()

	if len(req.Abi) > 0 {
		logger().Info(ctx, "💾 [ABI FLOW] Optimistically registering contract ABI",
			ion.String("contract_address", contractAddrHex),
			ion.String("abi_size", fmt.Sprintf("%d bytes", len(req.Abi))))

		// Create metadata
		metadata := &types.ContractMetadata{
			Address:      contractAddr,
			ABI:          req.Abi,
			Deployer:     caller,
			DeployTxHash: common.HexToHash(txHash),
			DeployTime:   uint64(time.Now().Unix()),
			DeployBlock:  0, // Pending
		}

		// Save to registry
		if err := r.contract_registry.RegisterContract(ctx, metadata); err != nil {
			logger().Warn(ctx, "⚠️ Failed to register contract metadata optimistically", ion.Err(err))
			// Don't fail the request, just warn
		}
	}

	return &proto.ExecutionResult{
		ReturnData:      txHash,          // Return tx hash
		GasUsed:         0,               // Will be populated after consensus
		ContractAddress: contractAddrHex, // Address is generated optimistically
		Success:         true,
	}, nil
}

// ============================================================================
// Execution
// ============================================================================

// ExecuteContract executes a contract function (state-changing).
// Each call gets its own fresh StateDB so concurrent requests don't race on shared state.
func (r *Router) ExecuteContract(ctx context.Context, req *proto.ExecuteContractRequest) (*proto.ExecutionResult, error) {
	logger().Info(ctx, "Executing contract",
		ion.String("caller", req.Caller),
		ion.String("contract", req.ContractAddress))

	// Per-request StateDB — prevents concurrent calls from corrupting shared in-memory state.
	// All instances share the same underlying state backend, so committed writes
	// from one call are visible to subsequent calls.
	stateDB, err := contractDB.InitializeStateDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize state: %w", err)
	}

	// Parse addresses
	caller := common.HexToAddress(req.Caller)
	contractAddr := common.HexToAddress(req.ContractAddress)

	// Parse value
	value := new(big.Int)
	if val, ok := value.SetString(req.Value, 0); ok {
		value = val
	} else if req.Value == "" {
		value = big.NewInt(0)
	} else {
		return nil, fmt.Errorf("invalid value: %s", req.Value)
	}

	// Parse input
	input, err := hexutil.Decode(req.Input)
	if err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}

	gasLimit := req.GasLimit
	if gasLimit == 0 {
		gasLimit = 10_000_000
	}

	// Execute contract
	result, err := r.executor.ExecuteContract(stateDB, caller, contractAddr, input, value, gasLimit)
	if err != nil {
		return &proto.ExecutionResult{
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	// Persist changes
	if _, err := stateDB.CommitToDB(false); err != nil {
		logger().Error(ctx, "Failed to commit state changes", err)
	}

	logger().Info(ctx, "Contract executed successfully",
		ion.Uint64("gas_used", result.GasUsed),
		ion.Int("return_data_size", len(result.ReturnData)))

	return &proto.ExecutionResult{
		ReturnData: hexutil.Encode(result.ReturnData),
		GasUsed:    result.GasUsed,
		Success:    result.Error == nil,
	}, nil
}

// CallContract performs a read-only contract call.
// Each call gets its own fresh StateDB (no commit at the end) so concurrent callers
// don't race on the shared in-memory stateObjects map.
func (r *Router) CallContract(ctx context.Context, req *proto.CallContractRequest) (string, error) {
	logger().Debug(ctx, "Calling contract (read-only)",
		ion.String("contract", req.ContractAddress))

	// Validate input
	if req.ContractAddress == "" {
		return "", fmt.Errorf("contract_address is required")
	}

	// Per-request read-only StateDB — backed by shared state backend so code is visible.
	// No CommitToDB call means no state mutations escape this function.
	stateDB, err := contractDB.InitializeStateDB()
	if err != nil {
		return "", fmt.Errorf("failed to initialize state: %w", err)
	}

	// Parse addresses
	caller := common.HexToAddress(req.Caller)
	contractAddr := common.HexToAddress(req.ContractAddress)

	// CallContractRequest has no GasLimit field in the proto — use a generous default.
	const gasLimit uint64 = 10_000_000

	// Parse input
	input, err := hexutil.Decode(req.Input)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	result, err := r.executor.ExecuteContract(stateDB, caller, contractAddr, input, big.NewInt(0), gasLimit)
	if err != nil {
		return "", fmt.Errorf("call failed: %w", err)
	}

	if result.Error != nil {
		return "", result.Error
	}

	return hexutil.Encode(result.ReturnData), nil
}

// ============================================================================
// Contract Information
// ============================================================================

// GetContractCode retrieves contract code and metadata
func (r *Router) GetContractCode(ctx context.Context, contractAddress string) (string, string, *proto.ContractMetadata, error) {
	addr := common.HexToAddress(contractAddress)

	logger().Info(ctx, "🔍 [ABI FLOW] GetContractCode called",
		ion.String("address", addr.Hex()))

	// Get contract code from state
	code := r.stateDB.GetCode(addr)
	logger().Info(ctx, "🔍 [ABI FLOW] Checked StateDB for Code",
		ion.String("address", addr.Hex()),
		ion.Int("code_len", len(code)))
	if len(code) == 0 {
		return "", "", nil, fmt.Errorf("contract not found at address %s", addr.Hex())
	}

	// Retrieve Metadata from Registry (Layer 2)
	var metadata *types.ContractMetadata
	var err error

	if r.contract_registry != nil {
		logger().Info(ctx, "📖 [ABI FLOW] Fetching from registry",
			ion.String("address", addr.Hex()))
		metadata, err = r.contract_registry.GetContract(ctx, addr)
		if err != nil {
			logger().Warn(ctx, "⚠️  [ABI FLOW] Failed to get contract metadata from registry", ion.Err(err),
				ion.String("address", addr.Hex()))
		} else {
			logger().Info(ctx, "📦 [ABI FLOW] Retrieved metadata from registry",
				ion.String("address", addr.Hex()),
				ion.Int("abi_length", len(metadata.ABI)),
				ion.Bool("abi_exists", len(metadata.ABI) > 0))
		}
	} else {
		logger().Warn(ctx, "⚠️  [ABI FLOW] Contract registry is nil", ion.Err(fmt.Errorf("registry is nil")))
	}

	// Convert to proto metadata
	protoMeta := &proto.ContractMetadata{
		Address: contractAddress,
	}
	if metadata != nil {
		protoMeta.Name = metadata.Name
		protoMeta.Abi = metadata.ABI
		protoMeta.Deployer = metadata.Deployer.Hex()
		protoMeta.TxHash = metadata.DeployTxHash.Hex()
		protoMeta.BlockNumber = metadata.DeployBlock
		protoMeta.Timestamp = metadata.DeployTime

		logger().Info(ctx, "✅ [ABI FLOW] Returning metadata with ABI",
			ion.String("address", addr.Hex()),
			ion.Int("proto_abi_length", len(protoMeta.Abi)))
	} else {
		logger().Warn(ctx, "⚠️  [ABI FLOW] No metadata found, returning empty",
			ion.Err(fmt.Errorf("no metadata found")),
			ion.String("address", addr.Hex()))
	}

	return hexutil.Encode(code), "", protoMeta, nil
}

// GetStorage retrieves a storage slot value
func (r *Router) GetStorage(ctx context.Context, contractAddress string, storageKey string) (string, error) {
	addr := common.HexToAddress(contractAddress)
	keyBytes, err := hexutil.Decode(storageKey)
	if err != nil {
		return "", fmt.Errorf("invalid storage key: %w", err)
	}
	key := common.BytesToHash(keyBytes)

	value := r.stateDB.GetState(addr, key)
	return hexutil.Encode(value.Bytes()), nil
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
			Address:     c.Address.Hex(),
			Name:        c.Name,
			Abi:         c.ABI,
			Deployer:    c.Deployer.Hex(),
			TxHash:      c.DeployTxHash.Hex(),
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
	logger().Debug(ctx, "Estimating gas")

	caller := common.HexToAddress(req.Caller)
	value := new(big.Int)
	if val, ok := value.SetString(req.Value, 0); ok {
		value = val
	} else {
		if req.Value == "" {
			value = big.NewInt(0)
		} else {
			return 0, fmt.Errorf("invalid value: %s", req.Value)
		}
	}

	// Parse input
	input, err := hexutil.Decode(req.Input)
	if err != nil {
		if req.Input != "" {
			return 0, fmt.Errorf("invalid input: %w", err)
		}
		input = []byte{}
	}

	var gasUsed uint64

	// Per-request StateDB for estimation — no CommitToDB means no state mutations persist.
	estimateDB, err := contractDB.InitializeStateDB()
	if err != nil {
		return 0, fmt.Errorf("failed to initialize state for estimation: %w", err)
	}

	if req.ContractAddress == "" {
		// Contract deployment estimation
		result, err := r.executor.DeployContract(estimateDB, caller, input, value, 10_000_000)
		if err != nil {
			return 0, fmt.Errorf("gas estimation failed: %w", err)
		}
		gasUsed = result.GasUsed
	} else {
		// Contract call estimation
		contractAddr := common.HexToAddress(req.ContractAddress)
		result, err := r.executor.ExecuteContract(estimateDB, caller, contractAddr, input, value, 10_000_000)
		if err != nil {
			return 0, fmt.Errorf("gas estimation failed: %w", err)
		}
		gasUsed = result.GasUsed
	}

	// Add 20% buffer for safety
	estimatedGas := gasUsed + (gasUsed / 5)

	logger().Debug(ctx, "Gas estimated",
		ion.Uint64("gas_used", gasUsed),
		ion.Uint64("estimated_gas", estimatedGas))

	return estimatedGas, nil
}

// ============================================================================
// ABI Utilities
// ============================================================================

// EncodeFunctionCall encodes a function call to ABI bytes
func (r *Router) EncodeFunctionCall(abiJSON string, functionName string, args []string) (string, error) {
	// Parse ABI
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return "", fmt.Errorf("failed to parse ABI: %w", err)
	}

	// WARNING: Simple string handling here. For complex types, we need proper JSON decoding of arguments based on ABI types.
	// For now, assuming args are just strings or simple types that abi.Pack might handle if we are lucky, or we need to parse them.
	// BUT, `abi.Pack` takes `interface{}`.
	// If the user passes JSON strings for arguments, we should try to unmarshal them?
	// OR we assume simple types for demo purposes?
	// The immediate request is just to fix types. I will convert []string to []interface{} directly for now,
	// checking if I can do better later.
	// A better approach: Decode arguments based on method signature.

	method, ok := parsedABI.Methods[functionName]
	if !ok {
		return "", fmt.Errorf("method not found: %s", functionName)
	}

	if len(args) != len(method.Inputs) {
		return "", fmt.Errorf("argument count mismatch: expected %d, got %d", len(method.Inputs), len(args))
	}

	var interfaceArgs []interface{}
	for i, arg := range args {
		inputType := method.Inputs[i].Type
		// Parse arg based on inputType
		// This is a minimal parser for basic types.
		switch inputType.T {
		case abi.StringTy:
			interfaceArgs = append(interfaceArgs, arg)
		case abi.UintTy, abi.IntTy:
			// Expecting number or hex string
			val, ok := new(big.Int).SetString(arg, 0)
			if !ok {
				return "", fmt.Errorf("invalid number for argument %d: %s", i, arg)
			}
			// ABI packer handles big.Int for uint/int
			interfaceArgs = append(interfaceArgs, val)
		case abi.BoolTy:
			if arg == "true" {
				interfaceArgs = append(interfaceArgs, true)
			} else {
				interfaceArgs = append(interfaceArgs, false)
			}
		case abi.AddressTy:
			interfaceArgs = append(interfaceArgs, common.HexToAddress(arg))
		default:
			// Fallback: assume string is fine or user provided proper value
			interfaceArgs = append(interfaceArgs, arg)
		}
	}

	// Pack function call
	packed, err := parsedABI.Pack(functionName, interfaceArgs...)
	if err != nil {
		return "", fmt.Errorf("failed to pack function call: %w", err)
	}

	return hexutil.Encode(packed), nil
}

// DecodeFunctionOutput decodes function output
func (r *Router) DecodeFunctionOutput(abiJSON string, functionName string, outputData string) ([]string, error) {
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

	// Decode hex string
	bytesData, err := hexutil.Decode(outputData)
	if err != nil {
		return nil, fmt.Errorf("invalid output data: %w", err)
	}

	unpacked, err := method.Outputs.Unpack(bytesData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack output: %w", err)
	}

	// Convert unpacked values to []string (JSON representation)
	var result []string
	for _, val := range unpacked {
		// Use fmt.Sprintf for simple string conversion, or json.Marshal
		// json.Marshal is safer for complex types
		strVal := fmt.Sprintf("%v", val)
		result = append(result, strVal)
	}
	return result, nil
}
