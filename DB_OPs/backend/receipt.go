package backend

// MODULE: DB_OPs/backend/receipt.go
// PURPOSE: Implement store.ReceiptStore — generate receipts on-the-fly from tx+block via ThebeReader.
// CORE DATA STRUCTURES: config.Receipt built from TransactionRecord + BlockRecord; no persistent state.
// TO MODIFY BEHAVIOR: edit generateReceiptFromRecords for different gas/log computation
// DO NOT: import legacy DB plumbing (PooledConnection-era packages)
// EXTENSION POINT: persist receipts → implement a ReceiptWriter and inject via New()

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	"gossipnode/config/utils"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

// GetReceipt generates a receipt on-the-fly by fetching the transaction and its containing block.
// Mirrors DB_OPs.GetReceiptByHash logic using ThebeReader instead of ImmuDB connections.
// Time: O(n) where n = number of transactions in the block (for cumulative gas computation).
func (b *thebeBackend) GetReceipt(ctx context.Context, txHash string) (*config.Receipt, error) {
	// Normalize hash — ensure 0x prefix
	normalizedHash := txHash
	if !strings.HasPrefix(strings.ToLower(txHash), "0x") {
		normalizedHash = "0x" + txHash
	}

	// Fetch transaction
	txRec, err := b.r.GetTransaction(ctx, normalizedHash)
	if err != nil {
		// Check contract receipt table for known-failed tx (status=0)
		cRec, cErr := b.r.GetContractReceipt(ctx, normalizedHash)
		if cErr == nil && cRec != nil && cRec.Status == 0 {
			return nil, nil // failed tx → null receipt (mirrors Facade_Receipts.go pattern)
		}
		return nil, fmt.Errorf("backend.GetReceipt(%s): transaction not found: %w", txHash, err)
	}

	// Fetch the containing block
	blockRec, err := b.r.GetBlock(ctx, txRec.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetReceipt(%s): get block %d: %w", txHash, txRec.BlockNumber, err)
	}

	// Fetch all transactions in block for cumulative gas calculation
	blockTxs, err := b.r.GetTransactionsByBlock(ctx, txRec.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetReceipt(%s): get block txs: %w", txHash, err)
	}

	// Compute cumulative gas used up to and including this transaction.
	// Uses GasLimit as proxy (mirrors Facade_Receipts.go — actual gas_used not stored separately).
	// Time: O(txIndex) — bounded by block tx count.
	var cumulativeGasUsed uint64
	txIndexInt := int(txRec.TxIndex)
	for i := 0; i <= txIndexInt && i < len(blockTxs); i++ {
		// GasLimit stored as string in TransactionRecord
		// parseUint64OrZero is a local helper to avoid panicking on empty/invalid values
		cumulativeGasUsed += parseUint64OrZero(blockTxs[i].GasLimit)
	}

	// Build minimal log entry mirroring Facade_Receipts.go pattern
	logs := []config.Log{}
	if txRec.FromAddr != "" {
		log := config.Log{
			BlockNumber: blockRec.BlockNumber,
			BlockHash:   common.HexToHash(blockRec.BlockHash),
			TxHash:      common.HexToHash(txRec.TxHash),
			TxIndex:     uint64(txRec.TxIndex),
			LogIndex:    uint64(txRec.TxIndex),
			Data:        []byte{0},
			Topics:      []common.Hash{},
			Removed:     false,
			Address:     common.HexToAddress(txRec.FromAddr),
		}
		logs = append(logs, log)
	}

	// Persisted EVM receipt (real status/gas/logs/contractAddress) overrides the
	// reconstruction when present; absent (plain transfers) keeps the defaults.
	status := uint64(1)
	gasUsed := parseUint64OrZero(txRec.GasLimit) // fallback proxy (plain-tx path)
	var contractAddress *common.Address
	if cr, cerr := b.GetContractReceipt(ctx, normalizedHash); cerr == nil && cr != nil && cr.Found {
		status = cr.Status
		gasUsed = cr.GasUsed
		contractAddress = cr.ContractAddress
		if len(cr.Logs) > 0 {
			logs = cr.Logs
		}
	}

	logsBloom := utils.GenerateLogsBloom(logs)

	return &config.Receipt{
		TxHash:            common.HexToHash(txRec.TxHash),
		BlockHash:         common.HexToHash(blockRec.BlockHash),
		BlockNumber:       blockRec.BlockNumber,
		TransactionIndex:  uint64(txRec.TxIndex),
		Status:            status,
		Type:              uint8(txRec.Type),
		GasUsed:           gasUsed,
		CumulativeGasUsed: cumulativeGasUsed,
		ContractAddress:   contractAddress,
		Logs:              logs,
		LogsBloom:         logsBloom,
		ZKProof:           nil,
		ZKStatus:          "",
	}, nil
}

// GetContractReceipt returns the persisted per-tx EVM outcome (written at apply
// time by the executor) mapped to the neutral store DTO. Found == false (nil
// error) when the tx has no persisted contract receipt (e.g. a plain transfer).
func (b *thebeBackend) GetContractReceipt(ctx context.Context, txHash string) (*store.ContractReceipt, error) {
	normalizedHash := txHash
	if !strings.HasPrefix(strings.ToLower(txHash), "0x") {
		normalizedHash = "0x" + txHash
	}

	rec, err := b.r.GetContractReceipt(ctx, normalizedHash)
	if err != nil {
		// A missing contract receipt (plain transfer) is not an error.
		m := strings.ToLower(err.Error())
		if strings.Contains(m, "no rows") || strings.Contains(m, "not found") {
			return &store.ContractReceipt{Found: false}, nil
		}
		return nil, fmt.Errorf("backend.GetContractReceipt(%s): %w", txHash, err)
	}
	if rec == nil {
		return &store.ContractReceipt{Found: false}, nil
	}

	out := &store.ContractReceipt{
		Found:        true,
		Status:       uint64(rec.Status),
		GasUsed:      parseUint64OrZero(rec.GasUsed),
		RevertReason: rec.RevertReason,
	}
	if rec.ContractAddress != nil && *rec.ContractAddress != "" {
		a := common.HexToAddress(*rec.ContractAddress)
		out.ContractAddress = &a
	}

	// Persisted logs are go-ethereum types.Log JSON — decode into that type, then
	// map to config.Log (their JSON field tags differ, so a direct unmarshal into
	// config.Log would drop most fields).
	if len(rec.Logs) > 0 {
		var glogs []*gethtypes.Log
		if json.Unmarshal(rec.Logs, &glogs) == nil {
			for _, gl := range glogs {
				if gl == nil {
					continue
				}
				out.Logs = append(out.Logs, config.Log{
					Address:     gl.Address,
					Topics:      gl.Topics,
					Data:        gl.Data,
					BlockNumber: gl.BlockNumber,
					BlockHash:   gl.BlockHash,
					TxHash:      gl.TxHash,
					TxIndex:     uint64(gl.TxIndex),
					LogIndex:    uint64(gl.Index),
					Removed:     gl.Removed,
				})
			}
		}
	}
	return out, nil
}

// parseUint64OrZero parses a decimal string to uint64; returns 0 on any error.
func parseUint64OrZero(s string) uint64 {
	if s == "" {
		return 0
	}
	var n uint64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
