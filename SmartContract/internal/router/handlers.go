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
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/transaction"
	"gossipnode/SmartContract/pkg/compiler"
	"gossipnode/SmartContract/pkg/types"
	"gossipnode/SmartContract/proto"
	pb "gossipnode/gETH/proto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog/log"
)

// ============================================================================
// Compilation
// ============================================================================

// CompileContract compiles Solidity source code
func (r *Router) CompileContract(sourceCode string) (*compiler.CompiledContract, error) {
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

	log.Info().
		Str("bytecode_size", fmt.Sprintf("%d bytes", len(contract.Bytecode))).
		Msg("Contract compiled successfully")

	return contract, nil
}

// ============================================================================
// Deployment
// ============================================================================

// DeployContract submits a contract deployment transaction to the network
func (r *Router) DeployContract(ctx context.Context, req *proto.DeployContractRequest) (*proto.ExecutionResult, error) {
	log.Info().
		Str("caller", req.Caller).
		Int("abi_length", len(req.Abi)).
		Bool("abi_provided", len(req.Abi) > 0).
		Msg("🚀 [CONSENSUS FLOW] DeployContract - Submitting transaction to network")

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

	// User Request: Use the specific account nonce from the DB (even if huge)
	// to allow transactions from accounts with timestamp-based nonces.
	//TODO: Fix with proper nonce fetching on mainnet
	var nonce uint64
	acc, err := DB_OPs.GetAccount(nil, caller)
	if err == nil && acc != nil {
		nonce = acc.Nonce
		log.Info().Uint64("account_nonce", nonce).Msg("Using specific account nonce from DB")
	} else {
		// Fetch the account to get the nonce directly (O(1)) instead of counting transactions (O(N))
		account, err := DB_OPs.GetAccount(nil, caller)
		if err != nil {
			// If account not found, nonce is 0
			nonce = 0
			log.Debug().Msgf("Account %s not found in DB, assuming nonce 0", caller.Hex())
		} else {
			nonce = account.Nonce
			log.Debug().Msgf("Account %s found in DB, nonce: %d", caller.Hex(), nonce)
		}
	}
	// Create and build contract deployment transaction
	// This helper handles both the internal config.Transaction creation and the Geth-compatible hash calculation
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

	// Sign the transaction with the deployer's private key.
	// private_key must be a 0x-prefixed 32-byte hex-encoded ECDSA key.
	if req.PrivateKey == "" {
		return nil, fmt.Errorf("private_key is required to sign the deployment transaction")
	}
	privKeyHex := strings.TrimPrefix(req.PrivateKey, "0x")
	privKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private_key: %w", err)
	}
	if err := transaction.SignTransaction(tx, privKey); err != nil {
		return nil, fmt.Errorf("failed to sign deployment transaction: %w", err)
	}
	// Recompute hash after signing (V/R/S are now populated)
	log.Info().
		Str("tx_hash", tx.Hash.Hex()).
		Msg("📝 Deployment transaction signed successfully")

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
		log.Error().Err(err).Msg("Failed to submit deployment transaction via gRPC")
		return &proto.ExecutionResult{
			Error:   fmt.Sprintf("Failed to submit transaction: %v", err),
			Success: false,
		}, nil
	}

	txHash := hex.EncodeToString(resp.TxHash)
	if !strings.HasPrefix(txHash, "0x") {
		txHash = "0x" + txHash
	}

	log.Info().
		Str("tx_hash", txHash).
		Msg(" Contract deployment transaction submitted successfully")

	// Note: Contract metadata (including ABI) will be registered by Processing.go
	// after consensus confirms the deployment.
	// HOWEVER, we should store it optimistically now so it's available immediately for lookups
	// or if Processing.go doesn't have the ABI in the payload.
	contractAddr := crypto.CreateAddress(caller, nonce)
	contractAddrHex := contractAddr.Hex()

	if len(req.Abi) > 0 {
		log.Info().
			Str("contract_address", contractAddrHex).
			Str("abi_size", fmt.Sprintf("%d bytes", len(req.Abi))).
			Msg("💾 [ABI FLOW] Optimistically registering contract ABI")

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
			log.Warn().Err(err).Msg("⚠️ Failed to register contract metadata optimistically")
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

// ExecuteContract executes a contract function (state-changing)
func (r *Router) ExecuteContract(ctx context.Context, req *proto.ExecuteContractRequest) (*proto.ExecutionResult, error) {
	log.Info().
		Str("caller", req.Caller).
		Str("contract", req.ContractAddress).
		Msg("Executing contract")

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

	// Execute contract
	result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, input, value, req.GasLimit)
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
		ReturnData: hexutil.Encode(result.ReturnData),
		GasUsed:    result.GasUsed,
		Success:    result.Error == nil,
	}, nil
}

// CallContract performs a read-only contract call
func (r *Router) CallContract(ctx context.Context, req *proto.CallContractRequest) (string, error) {
	log.Debug().Str("contract", req.ContractAddress).Msg("Calling contract (read-only)")

	// Parse addresses
	caller := common.HexToAddress(req.Caller)
	contractAddr := common.HexToAddress(req.ContractAddress)

	// Parse input
	input, err := hexutil.Decode(req.Input)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// Execute contract (read-only, no state changes)
	// For calls, we might want to use a snapshot or ensure no commit happens?
	// vm.StateDB usually handles avoiding commits if we don't call Commit.
	// We just ensure we don't call CommitToDB here.
	result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, input, big.NewInt(0), 10000000)
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

	log.Info().
		Str("address", addr.Hex()).
		Msg("🔍 [ABI FLOW] GetContractCode called")

	// Get contract code from state
	code := r.stateDB.GetCode(addr)
	log.Info().Str("address", addr.Hex()).Int("code_len", len(code)).Msg("🔍 [ABI FLOW] Checked StateDB for Code")
	if len(code) == 0 {
		return "", "", nil, fmt.Errorf("contract not found at address %s", addr.Hex())
	}

	// Retrieve Metadata from Registry (Layer 2)
	var metadata *types.ContractMetadata
	var err error

	if r.contract_registry != nil {
		log.Info().Str("address", addr.Hex()).Msg("📖 [ABI FLOW] Fetching from registry")
		metadata, err = r.contract_registry.GetContract(ctx, addr)
		if err != nil {
			log.Warn().
				Err(err).
				Str("address", addr.Hex()).
				Msg("⚠️  [ABI FLOW] Failed to get contract metadata from registry")
		} else {
			log.Info().
				Str("address", addr.Hex()).
				Int("abi_length", len(metadata.ABI)).
				Bool("abi_exists", len(metadata.ABI) > 0).
				Msg("📦 [ABI FLOW] Retrieved metadata from registry")
		}
	} else {
		log.Warn().Msg("⚠️  [ABI FLOW] Contract registry is nil")
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

		log.Info().
			Str("address", addr.Hex()).
			Int("proto_abi_length", len(protoMeta.Abi)).
			Msg("✅ [ABI FLOW] Returning metadata with ABI")
	} else {
		log.Warn().
			Str("address", addr.Hex()).
			Msg("⚠️  [ABI FLOW] No metadata found, returning empty")
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
	log.Debug().Msg("Estimating gas")

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

	// Clone stateDB for estimation?
	// Ideally we should use a snapshot or a read-only view to avoid contaminating state.
	// Snapshotting:
	snapshot := r.stateDB.Snapshot()
	defer r.stateDB.RevertToSnapshot(snapshot)

	if req.ContractAddress == "" {
		// Contract deployment
		result, err := r.executor.DeployContract(r.stateDB, caller, input, value, 10000000) // High gas limit for estimation
		if err != nil {
			return 0, fmt.Errorf("gas estimation failed: %w", err)
		}
		gasUsed = result.GasUsed
	} else {
		// Contract call
		contractAddr := common.HexToAddress(req.ContractAddress)
		result, err := r.executor.ExecuteContract(r.stateDB, caller, contractAddr, input, value, 10000000)
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
