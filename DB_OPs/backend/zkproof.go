package backend

import (
	"context"
	"fmt"

	"gossipnode/config"
	"gossipnode/DB_OPs/thebegateway"
)

// MODULE: DB_OPs/backend/zkproof.go
// PURPOSE: Implement store.ZKProofStore — chains WriteBlock+WriteZKProof+WriteSnapshot+WriteTransaction.
// CORE DATA STRUCTURES: config.ZKBlock decomposed into 4 separate records per write.
// TO MODIFY BEHAVIOR: change decomposition in StoreZKBlock
// DO NOT: import legacy DB plumbing (PooledConnection-era packages)
// EXTENSION POINT: add new ZK record types to the write chain

// StoreZKBlock writes block + ZK proof + snapshot + all transactions atomically (best-effort chain).
// Time: O(n) where n = number of transactions in the block.
func (b *thebeBackend) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("backend.StoreZKBlock: block is nil")
	}

	// 1. Write block record with ZK fields in ExtraData
	blockRec := toBlockRecordWithZK(block)
	if err := b.gw.WriteBlock(ctx, blockRec); err != nil {
		return fmt.Errorf("backend.StoreZKBlock(%d): WriteBlock: %w", block.BlockNumber, err)
	}

	// 2. Write ZK proof record
	proofRec := toZKProofRecord(block)
	if err := b.gw.WriteZKProof(ctx, proofRec); err != nil {
		return fmt.Errorf("backend.StoreZKBlock(%d): WriteZKProof: %w", block.BlockNumber, err)
	}

	// 3. Write snapshot record
	snapshotRec := &thebegateway.SnapshotRecord{
		BlockNumber: block.BlockNumber,
		BlockHash:   block.BlockHash.Hex(),
	}
	if err := b.gw.WriteSnapshot(ctx, snapshotRec); err != nil {
		return fmt.Errorf("backend.StoreZKBlock(%d): WriteSnapshot: %w", block.BlockNumber, err)
	}

	// 4. Write all transactions
	for i, tx := range block.Transactions {
		txCopy := tx // avoid loop variable capture
		txRec := toTransactionRecord(&txCopy, block.BlockNumber, i)
		if err := b.gw.WriteTransaction(ctx, txRec); err != nil {
			return fmt.Errorf("backend.StoreZKBlock(%d): WriteTransaction[%d]: %w", block.BlockNumber, i, err)
		}
	}

	return nil
}

// GetZKProof retrieves the ZK proof record for a block.
// Time: O(1) — cache-through PK lookup.
func (b *thebeBackend) GetZKProof(ctx context.Context, blockNumber uint64) (*thebegateway.ZKProofRecord, error) {
	rec, err := b.r.GetZKProof(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetZKProof(%d): %w", blockNumber, err)
	}
	return rec, nil
}
