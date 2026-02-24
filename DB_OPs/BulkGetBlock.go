package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/config"

	"sort"

	"github.com/JupiterMetaLabs/ion"
)

// GetBlocksRange retrieves a range of blocks from startBlock to endBlock (inclusive)
// utilizing the ImmuDB GetAll API for high-performance bulk retrieval.
func GetBlocksRange(mainDBClient *config.PooledConnection, startBlock, endBlock uint64) ([]*config.ZKBlock, error) {
	if startBlock > endBlock {
		return nil, fmt.Errorf("startBlock (%d) cannot be greater than endBlock (%d)", startBlock, endBlock)
	}

	// Setup context and logging
	ctx, cancel := context.WithCancel(context.Background()) // Increased timeout for bulk op
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var err error
	var shouldReturnConnection bool = false

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetBlocksRange", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved for bulk get",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("function", "DB_OPs.GetBlocksRange"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Debug(loggerCtx, "Returning Main DB connection after bulk get",
				ion.String("database", config.DBName),
				ion.String("function", "DB_OPs.GetBlocksRange"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	start := time.Now()
	defer func() {
		mainDBClient.Client.Logger.Debug(loggerCtx, "Time taken for GetBlocksRange",
			ion.Int64("time_taken", time.Since(start).Milliseconds()),
			ion.String("function", "DB_OPs.GetBlocksRange"),
		)
	}()

	totalBlocks := int(endBlock - startBlock + 1)
	if totalBlocks <= 0 {
		return []*config.ZKBlock{}, nil
	}

	// Prepare keys for GetAll request
	keys := make([][]byte, 0, totalBlocks)
	for block_number := startBlock; block_number <= endBlock; block_number++ {
		key := fmt.Sprintf("%s%d", PREFIX_BLOCK, block_number)
		keys = append(keys, []byte(key))
	}

	// Double check we have a client
	if mainDBClient == nil || mainDBClient.Client == nil || mainDBClient.Client.Client == nil {
		return nil, fmt.Errorf("invalid database client connection")
	}

	// Use GetAll to fetch all keys in one network round-trip.
	// Based on compiler feedback, the client GetAll method expects [][]byte directly.
	entriesList, err := mainDBClient.Client.Client.GetAll(ctx, keys)
	if err != nil {
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to execute GetAll for blocks",
			err,
			ion.Int("start_block", int(startBlock)),
			ion.Int("end_block", int(endBlock)),
			ion.Int("total_keys", len(keys)),
			ion.String("function", "DB_OPs.GetBlocksRange"),
		)
		return nil, fmt.Errorf("bulk retrieval failed: %w", err)
	}

	if entriesList == nil {
		return []*config.ZKBlock{}, nil
	}

	// Process results
	blocks := make([]*config.ZKBlock, 0, len(entriesList.Entries))

	for _, entry := range entriesList.Entries {
		if entry == nil || entry.Value == nil {
			continue
		}

		block := new(config.ZKBlock)
		if err := json.Unmarshal(entry.Value, block); err != nil {
			mainDBClient.Client.Logger.Error(loggerCtx, "Failed to unmarshal block data",
				err,
				ion.String("key", string(entry.Key)),
				ion.String("function", "DB_OPs.GetBlocksRange"),
			)
			return nil, fmt.Errorf("data corruption: failed to unmarshal block %s: %w", string(entry.Key), err)
		}
		blocks = append(blocks, block)
	}

	// Sort blocks by block number
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].BlockNumber < blocks[j].BlockNumber
	})

	mainDBClient.Client.Logger.Debug(loggerCtx, "Bulk get completed",
		ion.Int("requested", totalBlocks),
		ion.Int("found", len(blocks)),
		ion.String("function", "DB_OPs.GetBlocksRange"),
	)

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
// Returns nil slice strings when iteration is complete
func (it *BlockIterator) Next() ([]*config.ZKBlock, error) {
	if it.currentBlock > it.endBlock {
		return nil, nil
	}

	// Calculate batch end
	batchEnd := it.currentBlock + uint64(it.batchSize) - 1
	if batchEnd > it.endBlock {
		batchEnd = it.endBlock
	}

	// Use existing GetBlocksRange for the actual fetch
	// Pass nil for client to let GetBlocksRange handle acquiring/releasing connection if needed,
	// BUT since we are holding the client in the iterator, we should probably pass it if we have it?
	// The original code in GetBlocksRange handles nil client by acquiring one.
	// However, if we want to reuse the connection across batches, we should manage it carefully.
	// For now, let's just pass the client we have.

	blocks, err := GetBlocksRange(it.client, it.currentBlock, batchEnd)
	if err != nil {
		return nil, err
	}

	// Update current block pointer
	it.currentBlock = batchEnd + 1

	return blocks, nil
}
