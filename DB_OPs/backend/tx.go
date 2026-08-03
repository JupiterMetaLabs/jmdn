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
// DO NOT: import legacy DB plumbing (PooledConnection-era packages)
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

// GetTransactionsByAddressInRange retrieves transactions for an address within
// [fromBlock, toBlock] inclusive. Hot path for CatchUp ReconcileWithDeltas.
func (b *thebeBackend) GetTransactionsByAddressInRange(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.TransactionRecord, error) {
	recs, err := b.r.GetTransactionsByAddressInRange(ctx, address, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("backend.GetTransactionsByAddressInRange(%s, %d, %d): %w", address, fromBlock, toBlock, err)
	}
	return recs, nil
}

// SetTransactionStatus writes a minimal ContractReceiptRecord with the given status.
// Time: O(1) — single gateway write.
func (b *thebeBackend) SetTransactionStatus(ctx context.Context, txHash string, status int) error {
	rec := &thebegateway.ContractReceiptRecord{
		TxHash: txHash,
		Status: int16(status),
	}
	if err := b.gw.WriteContractReceipt(ctx, rec); err != nil {
		return fmt.Errorf("backend.SetTransactionStatus(%s, %d): %w", txHash, status, err)
	}
	return nil
}

// SetTxProcessing marks txHash as in-flight in BadgerDB KV ("-1" sentinel).
// Time: O(1) — single KV PutDerived via gateway.
func (b *thebeBackend) SetTxProcessing(ctx context.Context, txHash string) error {
	if err := b.gw.SetTxProcessing(ctx, txHash); err != nil {
		return fmt.Errorf("backend.SetTxProcessing(%s): %w", txHash, err)
	}
	return nil
}

// ClearTxProcessing removes the in-flight flag for txHash (empty tombstone in KV).
// Time: O(1) — single KV PutDerived via gateway.
func (b *thebeBackend) ClearTxProcessing(ctx context.Context, txHash string) error {
	if err := b.gw.ClearTxProcessing(ctx, txHash); err != nil {
		return fmt.Errorf("backend.ClearTxProcessing(%s): %w", txHash, err)
	}
	return nil
}

// IsTxProcessing returns true if txHash has an active in-flight flag in KV.
// Time: O(1) — single KV Get via reader.
func (b *thebeBackend) IsTxProcessing(ctx context.Context, txHash string) (bool, error) {
	processing, err := b.r.IsTxProcessing(ctx, txHash)
	if err != nil {
		return false, fmt.Errorf("backend.IsTxProcessing(%s): %w", txHash, err)
	}
	return processing, nil
}

// GetTransactionsPaginated returns a page of transactions ordered by block_number DESC.
func (b *thebeBackend) GetTransactionsPaginated(ctx context.Context, limit, offset int) ([]*thebegateway.TransactionRecord, error) {
	recs, err := b.r.GetTransactionsPaginated(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("backend.GetTransactionsPaginated: %w", err)
	}
	return recs, nil
}

// CountTransactions returns the total number of transactions in the SQL store.
func (b *thebeBackend) CountTransactions(ctx context.Context) (uint64, error) {
	n, err := b.r.CountTransactions(ctx)
	if err != nil {
		return 0, fmt.Errorf("backend.CountTransactions: %w", err)
	}
	return n, nil
}

// RefreshAccountTxStats recomputes tx_nonce and tx_count_sent for address.
func (b *thebeBackend) RefreshAccountTxStats(ctx context.Context, address string) error {
	if err := b.r.RefreshAccountTxStats(ctx, address); err != nil {
		return fmt.Errorf("backend.RefreshAccountTxStats(%s): %w", address, err)
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

	// Record the fee actually charged to the sender at ingest time.
	// Canonical math: config.GasFee (gasLimit × effective price). Readers
	// (historical balance, receipts, explorer) use this instead of re-deriving.
	rec.GasFeeWei = config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee).String()
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
