package evm

import (
	"context"
	"fmt"
	"time"

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/JupiterMetaLabs/ion"

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
	evmLogger().Info(context.Background(), "🚀 [EVM] Processing contract deployment",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("from", tx.From.Hex()))

	// Log the pre-increment nonce for debugging. The actual deployed address is
	// determined by evm.Create() which uses the nonce AFTER DeployContract increments it.
	currentNonce := stateDB.GetNonce(*tx.From)

	evmLogger().Info(context.Background(), "🔥 [EVM] Starting contract deployment (EVM will derive final address)",
		ion.Uint64("sender_nonce_before", currentNonce))

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
		evmLogger().Error(context.Background(), "❌ [EVM] Deployment failed", err,
			ion.String("tx_hash", tx.Hash.Hex()))
	} else {
		// evm.Create already called CreateAccount/CreateContract and SetCode internally.
		// No need to call CreateAccount again — doing so can interfere with the stateObject.

		evmLogger().Info(context.Background(), "✅ [EVM] Contract deployed successfully",
			ion.String("contract_address", contractAddr.Hex()),
			ion.Uint64("gas_used", gasUsed))

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
				evmLogger().Error(context.Background(), "❌ Failed to save contract metadata", err)
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
			evmLogger().Error(context.Background(), "❌ [EVM] failed to write deploy logs", err)
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
			evmLogger().Error(context.Background(), "❌ Failed to save transaction receipt", err)
		} else {
			evmLogger().Info(context.Background(), "🧾 Receipt stored successfully",
				ion.String("tx_hash", tx.Hash.Hex()))
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
	evmLogger().Info(context.Background(), "⚙️  [EVM] Processing contract execution",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()))

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
		evmLogger().Error(context.Background(), "❌ [EVM] Contract execution failed", err,
			ion.String("tx_hash", tx.Hash.Hex()))
		return nil, err
	}

	var logs []*ethtypes.Log
	if cdb, ok := stateDB.(*contractDB.ContractDB); ok {
		logs = cdb.Logs()
	}

	if len(logs) > 0 {
		if writeErr := DB_OPs.GlobalLogWriter.Write(logs); writeErr != nil {
			evmLogger().Error(context.Background(), "❌ [EVM] failed to write execution logs", writeErr)
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
			evmLogger().Error(context.Background(), "❌ Failed to save execution receipt", err)
		} else {
			evmLogger().Info(context.Background(), "🧾 Receipt & Logs stored successfully",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("log_count", len(logs)))
		}
	}

	evmLogger().Info(context.Background(), "✅ [EVM] Contract executed successfully",
		ion.String("contract", tx.To.Hex()),
		ion.Uint64("gas_used", result.GasUsed))

	return result, nil
}

// InitializeStateDB creates a StateDB for EVM execution.
// Delegates entirely to contractDB.InitializeStateDB() which manages the singletons.
func InitializeStateDB(chainID int) (contractDB.StateDB, error) {
	return contractDB.InitializeStateDB()
}
