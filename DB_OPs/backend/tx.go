package backend

import (
	"context"
	"fmt"
	"strconv"

	"gossipnode/config"
	"gossipnode/DB_OPs/thebegateway"
)

// MODULE: DB_OPs/backend/tx.go
// PURPOSE: Implement store.TxStore by delegating to ThebeGateway (writes) and ThebeReader (reads).
// CORE DATA STRUCTURES: config.Transaction ↔ thebegateway.TransactionRecord
// TO MODIFY BEHAVIOR: change field mapping in toTransactionRecord
// DO NOT: import ImmuDB, PooledConnection, or dualdb packages
// EXTENSION POINT: new tx fields → update toTransactionRecord

// StoreTransaction converts config.Transaction → TransactionRecord and writes.
// Time: O(1) — single gateway write.
func (b *thebeBackend) StoreTransaction(ctx context.Context, tx *config.Transaction, blockNumber uint64, txIndex int) error {
	if tx == nil {
		return fmt.Errorf("backend.StoreTransaction: tx is nil")
	}
	rec := toTransactionRecord(tx, blockNumber, txIndex)
	if err := b.gw.WriteTransaction(ctx, rec); err != nil {
		return fmt.Errorf("backend.StoreTransaction(%s): %w", tx.Hash.Hex(), err)
	}
	return nil
}

// GetTransaction retrieves a transaction by hash.
// Time: O(1) — cache-through PK lookup.
func (b *thebeBackend) GetTransaction(ctx context.Context, txHash string) (*thebegateway.TransactionRecord, error) {
	rec, err := b.r.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("backend.GetTransaction(%s): %w", txHash, err)
	}
	return rec, nil
}

// GetTransactionsByBlock retrieves all transactions in a block ordered by tx_index.
// Time: O(n) where n = number of transactions in the block.
func (b *thebeBackend) GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*thebegateway.TransactionRecord, error) {
	recs, err := b.r.GetTransactionsByBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetTransactionsByBlock(%d): %w", blockNumber, err)
	}
	return recs, nil
}

// GetTransactionsByAddress retrieves the most recent transactions for an address.
// Time: O(n) where n = limit — bounded SQL range scan.
func (b *thebeBackend) GetTransactionsByAddress(ctx context.Context, address string, limit int) ([]*thebegateway.TransactionRecord, error) {
	recs, err := b.r.GetLatestTransactionsByAddress(ctx, address, limit)
	if err != nil {
		return nil, fmt.Errorf("backend.GetTransactionsByAddress(%s, %d): %w", address, limit, err)
	}
	return recs, nil
}

// SetTransactionStatus writes a minimal ContractReceiptRecord with the given status.
// Time: O(1) — single gateway write.
func (b *thebeBackend) SetTransactionStatus(ctx context.Context, txHash string, status int) error {
	rec := &thebegateway.ContractReceiptRecord{
		TxHash:  txHash,
		Status:  int16(status),
	}
	if err := b.gw.WriteContractReceipt(ctx, rec); err != nil {
		return fmt.Errorf("backend.SetTransactionStatus(%s, %d): %w", txHash, status, err)
	}
	return nil
}

// toTransactionRecord converts config.Transaction → thebegateway.TransactionRecord.
func toTransactionRecord(tx *config.Transaction, blockNumber uint64, txIndex int) *thebegateway.TransactionRecord {
	rec := &thebegateway.TransactionRecord{
		TxHash:      tx.Hash.Hex(),
		BlockNumber: blockNumber,
		TxIndex:     int16(txIndex),
		Nonce:       strconv.FormatUint(tx.Nonce, 10),
		Type:        int16(tx.Type),
		Data:        tx.Data,
	}

	if tx.From != nil {
		rec.FromAddr = tx.From.Hex()
	}
	if tx.To != nil {
		s := tx.To.Hex()
		rec.ToAddr = &s
	}
	if tx.Value != nil {
		rec.ValueWei = tx.Value.String()
	}
	if tx.GasLimit > 0 {
		rec.GasLimit = strconv.FormatUint(tx.GasLimit, 10)
	}
	if tx.GasPrice != nil {
		rec.GasPriceWei = tx.GasPrice.String()
	}
	if tx.MaxFee != nil {
		rec.MaxFeeWei = tx.MaxFee.String()
	}
	if tx.MaxPriorityFee != nil {
		rec.MaxPriorityFeeWei = tx.MaxPriorityFee.String()
	}
	if tx.V != nil {
		if tx.V.IsUint64() {
			rec.SigV = tx.V.Uint64()
		}
	}
	if tx.R != nil {
		rec.SigR = tx.R.Text(16)
	}
	if tx.S != nil {
		rec.SigS = tx.S.Text(16)
	}

	return rec
}
