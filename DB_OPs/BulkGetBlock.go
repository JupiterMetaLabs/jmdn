package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
)

// GetBlocksRange retrieves a range of blocks from startBlock to endBlock (inclusive).
// NOTE: This was backed by ImmuDB GetAll. Migrated to ThebeDB in Phase 6 —
// use store.BlockStore.BulkGetBlocks instead. Returns error until migrated.
func GetBlocksRange(mainDBClient *config.PooledConnection, startBlock, endBlock uint64) ([]*config.ZKBlock, error) {
	if startBlock > endBlock {
		return nil, fmt.Errorf("startBlock (%d) cannot be greater than endBlock (%d)", startBlock, endBlock)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var err error
	var shouldReturnConnection = false

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetBlocksRange", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetBlocksRange: %w", err)
	}

	// Time: O(n) — single bulk SQL read (WHERE block_number BETWEEN $1 AND $2); n = endBlock-startBlock+1.
	records, err := h.BulkGetBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return nil, fmt.Errorf("GetBlocksRange: %w", err)
	}

	blocks := make([]*config.ZKBlock, 0, len(records))
	for _, r := range records {
		blk, convErr := blockRecordToZKBlock(r)
		if convErr != nil {
			return nil, fmt.Errorf("GetBlocksRange: convert block %d: %w", r.BlockNumber, convErr)
		}
		blocks = append(blocks, blk)
	}
	return blocks, nil
}

// BlockIterator handles paginated retrieval of blocks
type BlockIterator struct {
	client       *config.PooledConnection
	currentBlock uint64
	endBlock     uint64
	batchSize    int
}

// NewBlockIterator creates a new iterator for a range of blocks
// batchSize defaults to 1000 if set to 0 or less
func NewBlockIterator(client *config.PooledConnection, startBlock, endBlock uint64, batchSize int) *BlockIterator {
	if batchSize <= 0 {
		batchSize = 1000
	}
	return &BlockIterator{
		client:       client,
		currentBlock: startBlock,
		endBlock:     endBlock,
		batchSize:    batchSize,
	}
}

// Next retrieves the next batch of blocks
// Returns nil slice when iteration is complete
func (it *BlockIterator) Next() ([]*config.ZKBlock, error) {
	if it.currentBlock > it.endBlock {
		return nil, nil
	}

	// Calculate batch end
	batchEnd := it.currentBlock + uint64(it.batchSize) - 1
	if batchEnd > it.endBlock {
		batchEnd = it.endBlock
	}

	blocks, err := GetBlocksRange(it.client, it.currentBlock, batchEnd)
	if err != nil {
		return nil, err
	}

	// Update current block pointer
	it.currentBlock = batchEnd + 1

	return blocks, nil
}

// Ensure time is used (imported for context timeout in GetBlocksRange).
var _ = time.Second
