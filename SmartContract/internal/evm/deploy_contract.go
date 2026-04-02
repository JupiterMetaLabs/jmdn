package evm

import (
	"fmt"
	"gossipnode/SmartContract/internal/repository"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/SmartContract/internal/storage"
	"gossipnode/config"

	pbdid "gossipnode/DID/proto"

	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DeploymentResult contains the result of a contract deployment
type DeploymentResult struct {
	ContractAddress common.Address
	GasUsed         uint64
	Success         bool
	Error           error
}

// ProcessContractDeployment handles contract deployment during block processing
// This is called from messaging/BlockProcessing when a deployment transaction is encountered
// ProcessContractDeployment handles contract deployment during block processing
// This is called from messaging/BlockProcessing when a deployment transaction is encountered
func ProcessContractDeployment(
	tx *config.Transaction,
	stateDB state.StateDB,
	chainID int,
) (*DeploymentResult, error) {
	fmt.Println("=== [DEBUG] ProcessContractDeployment CALLED ===")

	log.Info().
		Str("tx_hash", tx.Hash.Hex()).
		Str("from", tx.From.Hex()).
		Msg("🚀 [EVM] Processing contract deployment")

	// StateDB is now injected, so we don't initialize it here
	// This supports the atomic execution flow required by consensus

	// Calculate the contract address (deterministic)
	// We MUST use the internal StateDB nonce which represents the actual current
	// number of executed transactions for this account in this block,
	// rather than the incoming tx.Nonce which may be outdated or duplicated
	currentNonce := stateDB.GetNonce(*tx.From)
	contractAddr := crypto.CreateAddress(*tx.From, currentNonce)

	log.Info().
		Str("contract_address", contractAddr.Hex()).
		Uint64("sender_nonce", currentNonce).
		Msg("🔥 [EVM] Calculated deterministic contract address based on StateDB nonce")

	// Create EVM executor
	executor := NewEVMExecutor(chainID)

	// Execute deployment
	log.Info().
		Str("contract_address", contractAddr.Hex()).
		Int("bytecode_size", len(tx.Data)).
		Msg("📝 [EVM] Executing deployment")

	result, err := executor.DeployContract(
		stateDB,
		*tx.From,
		tx.Data, // bytecode
		tx.Value,
		tx.GasLimit,
	)

	if err != nil {
		log.Error().
			Err(err).
			Str("tx_hash", tx.Hash.Hex()).
			Msg("❌ [EVM] Deployment failed")
		return &DeploymentResult{
			ContractAddress: contractAddr,
			Success:         false,
			Error:           err,
		}, err
	}

	// State changes are NOT committed here anymore.
	// They are kept in the injected StateDB and committed by the caller (BlockProcessing)
	// after all transactions are processed (and consensus is reached).

	// Create the contract account in the StateDB
	// This ensures the account exists for value transfers and future interactions
	log.Debug().Msg("👤 [EVM] Creating contract account in StateDB")
	stateDB.CreateAccount(contractAddr)
	// Note: No need to call DB_OPs.CreateAccount directly as calling CreateAccount on StateDB
	// and then Commiting StateDB will handle persistence.

	log.Info().
		Str("contract_address", contractAddr.Hex()).
		Uint64("gas_used", result.GasUsed).
		Msg("✅ [EVM] Contract deployed successfully")

	// ============================================================================
	// Persistence (Metadata & Receipt)
	// ============================================================================

	// 1. Save Contract Metadata
	contractMeta := state.ContractMetadata{
		ContractAddress:  contractAddr,
		CodeHash:         crypto.Keccak256Hash(tx.Data), // Approximate (should be runtime code hash, but init code hash is fine for now)
		CodeSize:         uint64(len(tx.Data)),          // Init code size
		DeployerAddress:  *tx.From,
		DeploymentTxHash: tx.Hash,
		DeploymentBlock:  uint64(0), // FIXME: Pass block number from context
		CreatedAt:        time.Now().UTC().Unix(),
	}

	if contractDB, ok := stateDB.(*state.ContractDB); ok {
		if err := contractDB.SetContractMetadata(contractAddr, contractMeta); err != nil {
			log.Error().Err(err).Msg("❌ Failed to save contract metadata")
			// We don't fail the transaction for metadata errors, just log it
		}
	}

	// 2. Save Transaction Receipt
	receipt := state.TransactionReceipt{
		TxHash:          tx.Hash,
		BlockNumber:     uint64(0), // FIXME: Pass block number
		TxIndex:         uint64(0), // FIXME: Pass tx index
		Status:          1,         // Success
		GasUsed:         result.GasUsed,
		ContractAddress: contractAddr,
		Logs:            nil, // Deployments typically don't emit logs unless init code does
		CreatedAt:       time.Now().UTC().Unix(),
	}

	if contractDB, ok := stateDB.(*state.ContractDB); ok {
		if err := contractDB.WriteReceipt(receipt); err != nil {
			log.Error().Err(err).Msg("❌ Failed to save transaction receipt")
		} else {
			log.Info().Str("tx_hash", tx.Hash.Hex()).Msg("🧾 Receipt stored successfully")
		}
	}

	return &DeploymentResult{
		ContractAddress: contractAddr,
		GasUsed:         result.GasUsed,
		Success:         true,
		Error:           nil,
	}, nil
}

// sharedKVStore is a singleton instance of the KVStore to prevent multiple resource locks
var sharedKVStore storage.KVStore

// SetSharedKVStore sets the global keys-value store instance
// This should be called by the main process (jmdn) initialization
func SetSharedKVStore(store storage.KVStore) {
	sharedKVStore = store
}

// InitializeStateDB creates a StateDB instance for EVM execution
// Uses existing connection pools and storage infrastructure
func InitializeStateDB(chainID int) (state.StateDB, error) {
	// TODO: Get gRPC clients from a global service registry instead of creating new connections
	// For now, use simplified initialization
	// The proper fix is to pass pre-initialized clients from the calling context

	// FIXME: This still creates new gRPC connections - needs to be refactored
	// to use dependency injection with pre-initialized clients
	log.Warn().Msg("⚠️  [EVM] State DB initialization needs refactoring - currently creates new gRPC conns")

	// Initialize gRPC connection to DID service
	// TODO: Get address from config.DID_SERVICE_ADDRESS
	didConn, err := grpc.NewClient(
		"localhost:15052", // FIXME: Make configurable
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to DID service: %w", err)
	}

	didClient := pbdid.NewDIDServiceClient(didConn)

	var storageDB storage.KVStore

	// Use shared store if available, otherwise create new one
	if sharedKVStore != nil {
		storageDB = sharedKVStore
		log.Debug().Msg("📊 [EVM] Using shared KVStore")
	} else {
		// Use storage factory to get KV store (uses singleton pattern internally)
		storageConfig := storage.Config{
			Type: storage.StoreTypePebble,
			Path: "./contract_storage_pebble",
		}

		storageDB, err = storage.NewKVStore(storageConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize contract storage: %w", err)
		}
	}

	// Create StateDB that proxies to gETH for account state and uses local storage for contracts
	repo := repository.NewPebbleAdapter(storageDB)
	stateDB := state.NewContractDB(didClient, repo)

	log.Debug().Msg("📊 [EVM] State DB initialized")
	return stateDB, nil
}

// ProcessContractExecution handles contract function calls during block processing
// This is called from messaging/BlockProcessing when a contract execution transaction is encountered
func ProcessContractExecution(
	tx *config.Transaction,
	stateDB state.StateDB,
	chainID int,
) (*ExecutionResult, error) {
	log.Info().
		Str("tx_hash", tx.Hash.Hex()).
		Str("from", tx.From.Hex()).
		Str("to", tx.To.Hex()).
		Msg("⚙️  [EVM] Processing contract execution")

	// StateDB is now injected, eliminating split-brain issues

	// Create EVM executor
	executor := NewEVMExecutor(chainID)

	// Execute contract call
	result, err := executor.ExecuteContract(
		stateDB,
		*tx.From,
		*tx.To,
		tx.Data, // ABI-encoded function call
		tx.Value,
		tx.GasLimit,
	)

	if err != nil {
		log.Error().
			Err(err).
			Str("tx_hash", tx.Hash.Hex()).
			Msg("❌ [EVM] Contract execution failed")
		return nil, err
	}

	// State changes are NOT committed here.
	// They are kept in the injected StateDB and committed by the caller (BlockProcessing)

	log.Info().
		Str("contract", tx.To.Hex()).
		Uint64("gas_used", result.GasUsed).
		Msg("✅ [EVM] Contract executed successfully")

	// ============================================================================
	// Persistence (Receipt & Logs)
	// ============================================================================

	var logs []*ethtypes.Log
	if contractDB, ok := stateDB.(*state.ContractDB); ok {
		// Capture logs before they might be cleared (though they persist in StateDB until next block/tx reset)
		logs = contractDB.Logs()
	}

	// Save Transaction Receipt
	receipt := state.TransactionReceipt{
		TxHash:          tx.Hash,
		BlockNumber:     uint64(0), // FIXME: Pass block number
		TxIndex:         uint64(0), // FIXME: Pass tx index
		Status:          1,         // Success
		GasUsed:         result.GasUsed,
		ContractAddress: *tx.To, // The contract we called
		Logs:            logs,
		CreatedAt:       time.Now().UTC().Unix(),
	}

	if contractDB, ok := stateDB.(*state.ContractDB); ok {
		if err := contractDB.WriteReceipt(receipt); err != nil {
			log.Error().Err(err).Msg("❌ Failed to save execution receipt")
		} else {
			log.Info().
				Str("tx_hash", tx.Hash.Hex()).
				Int("log_count", len(logs)).
				Msg("🧾 Receipt & Logs stored successfully")
		}
	}

	return result, nil
}
