package backend

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
	"gossipnode/DB_OPs/thebegateway"
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
		BlockNumber: b.BlockNumber,                    // direct field
		BlockHash:   b.BlockHash.Hex(),                // common.Hash → 0x-prefixed hex string
		ParentHash:  b.PrevHash.Hex(),                 // ZKBlock.PrevHash → BlockRecord.ParentHash
		Timestamp:   time.Unix(b.Timestamp, 0).UTC(), // int64 epoch-seconds → time.Time
		TxsRoot:     b.TxnsRoot,                       // ZKBlock.TxnsRoot → BlockRecord.TxsRoot
		StateRoot:   b.StateRoot.Hex(),                // common.Hash → hex string
		LogsBloom:   b.LogsBloom,                      // []byte → []byte direct
		GasLimit:    b.GasLimit,                       // uint64 direct
		GasUsed:     b.GasUsed,                        // uint64 direct
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
