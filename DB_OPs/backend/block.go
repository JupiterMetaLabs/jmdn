package backend

import (
	"context"
	"fmt"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// StoreBlock converts a config.ZKBlock to a thebegateway.BlockRecord and writes it.
// Time: O(1) — single gateway write.
func (b *thebeBackend) StoreBlock(ctx context.Context, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("backend.StoreBlock: block is nil")
	}

	rec := toBlockRecord(block)
	if err := b.gw.WriteBlock(ctx, rec); err != nil {
		return fmt.Errorf("backend.StoreBlock(%d): %w", block.BlockNumber, err)
	}
	return nil
}

// GetBlock retrieves a block by block number.
// Time: O(1) — cache-through PK lookup.
func (b *thebeBackend) GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error) {
	rec, err := b.r.GetBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlock(%d): %w", blockNumber, err)
	}
	return rec, nil
}

// GetBlockByHash retrieves a block by its hash.
// Delegates to ThebeReader.GetBlockByHash (Phase 2.0 — hash-indexed SQL query).
// Time: O(1) — cache-through hash-index lookup.
func (b *thebeBackend) GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error) {
	rec, err := b.r.GetBlockByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlockByHash(%q): %w", hash, err)
	}
	return rec, nil
}

// GetLatestBlockNumber returns the block number of the most recently stored block.
// Delegates to ThebeReader.GetLatestBlock and extracts BlockNumber.
// Time: O(1) — cache-through query.
func (b *thebeBackend) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	rec, err := b.r.GetLatestBlock(ctx)
	if err != nil {
		return 0, fmt.Errorf("backend.GetLatestBlockNumber: %w", err)
	}
	return rec.BlockNumber, nil
}

// BulkGetBlocks retrieves all blocks in [from, to] inclusive via a single SQL range query.
// Delegates to ThebeReader.BulkGetBlocks (Phase 2.0).
// Time: O(n) where n = to-from+1 — single SQL scan, not n individual lookups.
func (b *thebeBackend) BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	if from > to {
		return nil, fmt.Errorf("backend.BulkGetBlocks: from(%d) > to(%d)", from, to)
	}
	recs, err := b.r.BulkGetBlocks(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("backend.BulkGetBlocks(%d,%d): %w", from, to, err)
	}
	return recs, nil
}

// toBlockRecord converts a config.ZKBlock to thebegateway.BlockRecord.
// Field-by-field mapping documented inline.
func toBlockRecord(b *config.ZKBlock) *thebegateway.BlockRecord {
	rec := &thebegateway.BlockRecord{
		BlockNumber: b.BlockNumber,                   // direct field
		BlockHash:   b.BlockHash.Hex(),               // common.Hash → 0x-prefixed hex string
		ParentHash:  b.PrevHash.Hex(),                // ZKBlock.PrevHash → BlockRecord.ParentHash
		Timestamp:   time.Unix(b.Timestamp, 0).UTC(), // int64 epoch-seconds → time.Time
		TxsRoot:     b.TxnsRoot,                      // ZKBlock.TxnsRoot → BlockRecord.TxsRoot
		StateRoot:   b.StateRoot.Hex(),               // common.Hash → hex string
		LogsBloom:   b.LogsBloom,                     // []byte → []byte direct
		GasLimit:    b.GasLimit,                      // uint64 direct
		GasUsed:     b.GasUsed,                       // uint64 direct
	}

	// CoinbaseAddr: nil pointer → empty string; present → 0x-prefixed hex
	if b.CoinbaseAddr != nil {
		rec.CoinbaseAddr = b.CoinbaseAddr.Hex()
	}

	// ZKVMAddr: nil pointer → empty string; present → 0x-prefixed hex
	if b.ZKVMAddr != nil {
		rec.ZKVMAddr = b.ZKVMAddr.Hex()
	}

	// ExtraData: ZKBlock.ExtraData is a raw string; store as map for JSONB
	if b.ExtraData != "" {
		rec.ExtraData = map[string]any{"raw": b.ExtraData}
	}

	// CommitteeCertificate: persist the verified committee vote set (JSON) so it
	// survives past the ephemeral gossip envelope and is re-verifiable on sync
	// (P-cert / ThebeSync). Stashed in ExtraData JSONB; applyBlock marshals the
	// whole map, so any key here round-trips. This is the FIRST block write in
	// StoreZKBlock (before toBlockRecordWithZK), and blocks are append-only with
	// ON CONFLICT DO NOTHING, so the cert must be set here to win the projection.
	if b.CommitteeCertificate != "" {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		rec.ExtraData["committee_certificate"] = b.CommitteeCertificate
	}

	return rec
}

// toBlockRecordWithZK extends toBlockRecord with ZK proof fields for StoreZKBlock.
// Returns a BlockRecord with ZK status embedded in ExtraData.
func toBlockRecordWithZK(b *config.ZKBlock) *thebegateway.BlockRecord {
	rec := toBlockRecord(b)
	// Embed ZK fields in ExtraData since BlockRecord has no dedicated ZK columns.
	if rec.ExtraData == nil {
		rec.ExtraData = map[string]any{}
	}
	rec.ExtraData["proof_hash"] = b.ProofHash
	rec.ExtraData["zk_status"] = b.Status
	return rec
}

// toZKProofRecord converts a config.ZKBlock to thebegateway.ZKProofRecord.
func toZKProofRecord(b *config.ZKBlock) *thebegateway.ZKProofRecord {
	// Commitment: []uint32 → []byte (big-endian uint32 packing)
	commitment := commitmentToBytes(b.Commitment)
	return &thebegateway.ZKProofRecord{
		BlockNumber: b.BlockNumber,
		ProofHash:   b.ProofHash,
		StarkProof:  b.StarkProof,
		Commitment:  commitment,
	}
}

// commitmentToBytes packs []uint32 commitment into []byte (4 bytes per element, big-endian).
// Time: O(n) where n = len(commitment).
func commitmentToBytes(c []uint32) []byte {
	if len(c) == 0 {
		return nil
	}
	out := make([]byte, len(c)*4)
	for i, v := range c {
		out[i*4] = byte(v >> 24)
		out[i*4+1] = byte(v >> 16)
		out[i*4+2] = byte(v >> 8)
		out[i*4+3] = byte(v)
	}
	return out
}

// StoreL1Finality appends an L1 finality record through the gateway
// (canonical log → projector → l1_finality table).
func (b *thebeBackend) StoreL1Finality(ctx context.Context, rec *thebegateway.L1FinalityRecord) error {
	if rec == nil || rec.Confirmation == "" {
		return fmt.Errorf("backend.StoreL1Finality: nil or empty confirmation")
	}
	if err := b.gw.WriteL1Finality(ctx, rec); err != nil {
		return fmt.Errorf("backend.StoreL1Finality(%s): %w", rec.Confirmation, err)
	}
	return nil
}

// GetL1FinalityForBlock returns the latest L1 commit covering blockNumber.
func (b *thebeBackend) GetL1FinalityForBlock(ctx context.Context, blockNumber uint64) (*thebegateway.L1FinalityRecord, error) {
	rec, err := b.r.GetL1FinalityForBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetL1FinalityForBlock(%d): %w", blockNumber, err)
	}
	return rec, nil
}

// GetBlocksByRewardAddress returns blocks where address is coinbase or ZKVM.
// Used by historical balance reconstruction.
func (b *thebeBackend) GetBlocksByRewardAddress(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.BlockRecord, error) {
	recs, err := b.r.GetBlocksByRewardAddress(ctx, address, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlocksByRewardAddress(%s, %d, %d): %w", address, fromBlock, toBlock, err)
	}
	return recs, nil
}

// PutSyncKV / GetSyncKV expose the gateway's durable sync-state KV
// (tx markers, applied anchor, latest_block marker — F-train modules).
func (b *thebeBackend) PutSyncKV(key string, value []byte) error {
	return b.gw.PutSyncKV(key, value)
}

func (b *thebeBackend) GetSyncKV(key string) ([]byte, error) {
	return b.gw.GetSyncKV(key)
}
