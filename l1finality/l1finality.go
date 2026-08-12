// Package l1finality centralizes the logic for applying L1 rollup-commitment
// finality data to local block records. It exists because that logic was
// previously duplicated across three call sites — the HTTP ingestion
// endpoints in Block/Server.go and two independent gossip-broadcast
// receivers (AVC/BuddyNodes/MessagePassing/Service and
// .../PubSubConnector) — which meant any future fix (validation, a cap,
// bug fix) had to be applied three times by hand or one path would silently
// stay stale. All three now call into this package instead.
package l1finality

import (
	"fmt"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// MaxRangeSpan caps how many blocks a single l1-commit-range request/message
// may cover. Without this, a malformed or oversized range (e.g. spanning
// billions of blocks) would drive an unbounded sequential DB read/write loop
// per request.
const MaxRangeSpan = 10_000

// CommitPayload is the body/message for a single-block L1 commit.
type CommitPayload struct {
	BlockNumber   uint64 `json:"block_number"`
	L1TxHash      string `json:"l1_tx_hash"`
	L1BlockNumber uint64 `json:"l1_block_number"`
}

// Validate checks required fields. It does not check whether the block
// exists locally — that's a separate, non-fatal outcome handled by ApplyCommit.
func (p CommitPayload) Validate() error {
	if p.BlockNumber == 0 || p.L1TxHash == "" {
		return fmt.Errorf("block_number and l1_tx_hash are required")
	}
	return nil
}

// RangePayload is the body/message for a batched L1 commit across a block range.
type RangePayload struct {
	StartBlock    uint64 `json:"start_block"`
	EndBlock      uint64 `json:"end_block"`
	L1TxHash      string `json:"l1_tx_hash"`
	L1BlockNumber uint64 `json:"l1_block_number"`
}

// Validate checks required fields and enforces MaxRangeSpan.
func (p RangePayload) Validate() error {
	if p.StartBlock == 0 || p.EndBlock < p.StartBlock || p.L1TxHash == "" {
		return fmt.Errorf("start_block, end_block >= start_block, and l1_tx_hash are required")
	}
	if span := p.EndBlock - p.StartBlock + 1; span > MaxRangeSpan {
		return fmt.Errorf("range too large: %d blocks requested, max %d per request", span, MaxRangeSpan)
	}
	return nil
}

// ApplyCommit fetches the block, stamps L1 finality fields, and writes it back.
// found=false (with err=nil) means the block does not exist locally yet —
// expected for a peer that hasn't synced that far, and callers should treat
// it as non-fatal rather than an error.
// THEBEDB: blocks rows are append-only (immutable), so L1 finality is stored
// in the dedicated l1_finality table (DB_OPs.StoreL1CommitRange) rather than
// by mutating the block record. Block reads hydrate L1TxHash/L1BlockNumber
// from that table (GetZKBlockByNumber), so consumers see the same fields.
func ApplyCommit(conn *config.PooledConnection, p CommitPayload) (found bool, err error) {
	if _, err := DB_OPs.GetZKBlockByNumber(conn, p.BlockNumber); err != nil {
		return false, nil // block not synced locally yet — non-fatal
	}
	if err := DB_OPs.StoreL1CommitRange(p.L1TxHash, p.L1BlockNumber, p.BlockNumber, p.BlockNumber); err != nil {
		return true, fmt.Errorf("store l1 commit for block %d: %w", p.BlockNumber, err)
	}
	return true, nil
}

// ApplyRange applies the same L1 tx hash/block number across every block in
// [StartBlock, EndBlock], skipping (not failing on) any block not found
// locally — a peer may not have synced that far yet. Callers must call
// p.Validate() first; ApplyRange itself does not re-check MaxRangeSpan.
func ApplyRange(conn *config.PooledConnection, p RangePayload) (updated, skipped int) {
	// Determine which blocks in the range exist locally — the l1_finality row
	// covers exactly those; blocks synced later hydrate from the same row, so
	// a skipped block self-heals on the next ApplyRange gossip replay.
	var present []uint64
	for blockNum := p.StartBlock; blockNum <= p.EndBlock; blockNum++ {
		if _, err := DB_OPs.GetZKBlockByNumber(conn, blockNum); err != nil {
			skipped++
			continue
		}
		present = append(present, blockNum)
	}
	if len(present) == 0 {
		return 0, skipped
	}
	// One append-only l1_finality record for the whole confirmed range —
	// StoreL1CommitRange records every block in [start, end]; reads join on
	// block_numbers containment, so blocks missing locally today are covered
	// the moment they sync.
	if err := DB_OPs.StoreL1CommitRange(p.L1TxHash, p.L1BlockNumber, p.StartBlock, p.EndBlock); err != nil {
		return 0, skipped + len(present)
	}
	updated = len(present)
	return updated, skipped
}
