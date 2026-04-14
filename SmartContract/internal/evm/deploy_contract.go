package evm

import (
	"fmt"
	"time"

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
)

// DeploymentResult contains the result of a contract deployment.
type DeploymentResult struct {
	ContractAddress common.Address
	GasUsed         uint64
	Success         bool
	Error           error
}

// ProcessContractDeployment handles contract deployment during block processing.
// The stateDB must already be initialised and injected by the caller (BlockProcessing).
func ProcessContractDeployment(
	tx *config.Transaction,
	stateDB contractDB.StateDB,
	chainID int,
) (*DeploymentResult, error) {
	log.Info().
		Str("tx_hash", tx.Hash.Hex()).
		Str("from", tx.From.Hex()).
		Msg("🚀 [EVM] Processing contract deployment")

	// Log the pre-increment nonce for debugging. The actual deployed address is
	// determined by evm.Create() which uses the nonce AFTER DeployContract increments it.
	currentNonce := stateDB.GetNonce(*tx.From)

	log.Info().
		Uint64("sender_nonce_before", currentNonce).
		Msg("🔥 [EVM] Starting contract deployment (EVM will derive final address)")

	executor := NewEVMExecutor(chainID)

	result, err := executor.DeployContract(
		stateDB,
		*tx.From,
		tx.Data, // bytecode
		tx.Value,
		tx.GasLimit,
	)

	success := err == nil
	var revertReason string
	var gasUsed uint64

	// The authoritative contract address comes from the EVM result.
	// evm.Create derives the address from the caller's nonce at call-time (before the
	// post-call increment in DeployContract), so result.ContractAddr == crypto.CreateAddress(from, currentNonce).
	var contractAddr common.Address
	if result != nil {
		contractAddr = result.ContractAddr
		gasUsed = result.GasUsed
		if !success && len(result.ReturnData) > 0 {
			revertReason = fmt.Sprintf("0x%x", result.ReturnData)
		}
	}

	if !success {
		log.Error().
			Err(err).
			Str("tx_hash", tx.Hash.Hex()).
			Msg("❌ [EVM] Deployment failed")
	} else {
		// evm.Create already called CreateAccount/CreateContract and SetCode internally.
		// No need to call CreateAccount again — doing so can interfere with the stateObject.

		log.Info().
			Str("contract_address", contractAddr.Hex()).
			Uint64("gas_used", gasUsed).
			Msg("✅ [EVM] Contract deployed successfully")

		// Save contract metadata.
		meta := contractDB.ContractMetadata{
			ContractAddress:  contractAddr,
			CodeHash:         crypto.Keccak256Hash(tx.Data),
			CodeSize:         uint64(len(tx.Data)),
			DeployerAddress:  *tx.From,
			DeploymentTxHash: tx.Hash,
			DeploymentBlock:  0, // FIXME: pass block number from context
			CreatedAt:        time.Now().UTC().Unix(),
		}

		if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
			if err := cdb.SetContractMetadata(contractAddr, meta); err != nil {
				log.Error().Err(err).Msg("❌ Failed to save contract metadata")
			}
		}
	}

	// Capture logs from init code.
	var deployLogs []*ethtypes.Log
	if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
		deployLogs = cdb.Logs()
	}
	if len(deployLogs) > 0 {
		if err := DB_OPs.GlobalLogWriter.Write(deployLogs); err != nil {
			log.Error().Err(err).Msg("❌ [EVM] failed to write deploy logs")
		}
	}

	// Save transaction receipt.
	status := uint64(0)
	if success {
		status = 1
	}

	receipt := contractDB.TransactionReceipt{
		TxHash:      tx.Hash,
		BlockNumber: 0, // FIXME: pass block number
		TxIndex:     0, // FIXME: pass tx index
		Status:      status,
		GasUsed:     gasUsed,
		Logs:        deployLogs,
		CreatedAt:   time.Now().UTC().Unix(),
	}

	if success {
		receipt.ContractAddress = contractAddr
	} else if revertReason != "" {
		receipt.RevertReason = revertReason
	}

	if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
		if err := cdb.WriteReceipt(receipt); err != nil {
			log.Error().Err(err).Msg("❌ Failed to save transaction receipt")
		} else {
			log.Info().Str("tx_hash", tx.Hash.Hex()).Msg("🧾 Receipt stored successfully")
		}
	}

	return &DeploymentResult{
		ContractAddress: contractAddr,
		GasUsed:         gasUsed,
		Success:         success,
		Error:           err,
	}, err
}

// ProcessContractExecution handles contract function calls during block processing.
func ProcessContractExecution(
	tx *config.Transaction,
	stateDB contractDB.StateDB,
	chainID int,
) (*ExecutionResult, error) {
	log.Info().
		Str("tx_hash", tx.Hash.Hex()).
		Str("from", tx.From.Hex()).
		Str("to", tx.To.Hex()).
		Msg("⚙️  [EVM] Processing contract execution")

	executor := NewEVMExecutor(chainID)

	result, err := executor.ExecuteContract(
		stateDB,
		*tx.From,
		*tx.To,
		tx.Data,
		tx.Value,
		tx.GasLimit,
	)

	if err != nil {
		log.Error().Err(err).Str("tx_hash", tx.Hash.Hex()).Msg("❌ [EVM] Contract execution failed")
		return nil, err
	}

	var logs []*ethtypes.Log
	if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
		logs = cdb.Logs()
	}

	if len(logs) > 0 {
		if writeErr := DB_OPs.GlobalLogWriter.Write(logs); writeErr != nil {
			log.Error().Err(writeErr).Msg("❌ [EVM] failed to write execution logs")
		}
	}

	receipt := contractDB.TransactionReceipt{
		TxHash:          tx.Hash,
		BlockNumber:     0, // FIXME
		TxIndex:         0, // FIXME
		Status:          1,
		GasUsed:         result.GasUsed,
		ContractAddress: *tx.To,
		Logs:            logs,
		CreatedAt:       time.Now().UTC().Unix(),
	}

	if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
		if err := cdb.WriteReceipt(receipt); err != nil {
			log.Error().Err(err).Msg("❌ Failed to save execution receipt")
		} else {
			log.Info().
				Str("tx_hash", tx.Hash.Hex()).
				Int("log_count", len(logs)).
				Msg("🧾 Receipt & Logs stored successfully")
		}
	}

	log.Info().
		Str("contract", tx.To.Hex()).
		Uint64("gas_used", result.GasUsed).
		Msg("✅ [EVM] Contract executed successfully")

	return result, nil
}

// InitializeStateDB creates a StateDB for EVM execution.
// Delegates entirely to contractDB.InitializeStateDB() which manages the singletons.
func InitializeStateDB(chainID int) (contractDB.StateDB, error) {
	return contractDB.InitializeStateDB()
}
