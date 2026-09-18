package backend

import (
	"context"
	"fmt"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// MODULE: DB_OPs/backend/zkproof.go
// PURPOSE: Implement store.ZKProofStore — chains WriteBlock+WriteZKProof+WriteSnapshot+WriteTransaction.
// CORE DATA STRUCTURES: config.ZKBlock decomposed into 4 separate records per write.
// TO MODIFY BEHAVIOR: change decomposition in StoreZKBlock
// DO NOT: import legacy DB plumbing (PooledConnection-era packages)
// EXTENSION POINT: add new ZK record types to the write chain

// StoreZKBlock writes block + snapshot + (optional) ZK proof + all transactions
// as an idempotent chain. All gateway writers are upserts (ON CONFLICT DO
// NOTHING/UPDATE), so a re-store completes a partially-projected block rather
// than failing (deliverable E).
//
// D-64 (SEV-1): the snapshot record is written for EVERY block, proof or not.
// `snapshots` is the FK parent of `transactions` (fk_txn_snapshot), so gating it
// on a ZK proof — as the old code did — left proofless blocks (every catch-up
// block, whose served copy carries no proof) with a `blocks` row, NO `snapshots`
// row, and every transaction insert violating the FK. The correct invariant is
// block → snapshot → [zkproof] → transactions, unconditionally.
//
// The ZK proof, in contrast, IS conditional: zk_proofs.proof_hash is
// `CHAR(66) NOT NULL UNIQUE`, so writing an empty proof for a proofless block
// would violate NOT NULL / collide on the empty string. Only write it when the
// block actually carries one.
//
// Time: O(n) where n = number of transactions in the block.
func (b *thebeBackend) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("backend.StoreZKBlock: block is nil")
	}

	// 1. Block record (parent of snapshot, zk_proofs and transactions). Carries
	//    the ZK fields in ExtraData so a proof-bearing block round-trips.
	blockRec := toBlockRecordWithZK(block)
	if err := b.gw.WriteBlock(ctx, blockRec); err != nil {
		return fmt.Errorf("backend.StoreZKBlock(%d): WriteBlock: %w", block.BlockNumber, err)
	}

	// 2. Snapshot record — ALWAYS (FK parent of transactions). See D-64 above.
	snapshotRec := &thebegateway.SnapshotRecord{
		BlockNumber: block.BlockNumber,
		BlockHash:   block.BlockHash.Hex(),
	}
	if err := b.gw.WriteSnapshot(ctx, snapshotRec); err != nil {
		return fmt.Errorf("backend.StoreZKBlock(%d): WriteSnapshot: %w", block.BlockNumber, err)
	}

	// 3. ZK proof record — only when the block carries one (proof_hash is NOT
	//    NULL UNIQUE; an empty proof would fail the constraint).
	if block.ProofHash != "" || len(block.StarkProof) > 0 {
		proofRec := toZKProofRecord(block)
		if err := b.gw.WriteZKProof(ctx, proofRec); err != nil {
			return fmt.Errorf("backend.StoreZKBlock(%d): WriteZKProof: %w", block.BlockNumber, err)
		}
	}

	// 4. Transactions (child of snapshots via fk_txn_snapshot).
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
