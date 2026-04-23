package NodeInfo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"gossipnode/DB_OPs"
)

type dbBlockIterator struct {
	current   uint64
	tail      uint64
	start     uint64
	end       uint64
	batchsize uint64
	tailDone  bool
}

// Time Complexity: O(1)
func (sync *sync_struct) NewBlockIterator(start, end uint64, batchsize int) types.BlockIterator {
	return &dbBlockIterator{
		current:   start,
		tail:      end,
		start:     start,
		end:       end,
		batchsize: uint64(batchsize),
		tailDone:  false,
	}
}

// Time Complexity: O(N) where N is the batch size
func (i *dbBlockIterator) Next() ([]*types.ZKBlock, error) {
	if i.current > i.end {
		return nil, nil
	}

	batchEnd := i.current + i.batchsize - 1
	if batchEnd > i.end {
		batchEnd = i.end
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, err
	}

	blocks, err := DB_OPs.GetBlocksRange(conn, i.current, batchEnd)
	if err != nil {
		return nil, err
	}

	i.current = batchEnd + 1

	var ptrs []*types.ZKBlock
	for _, b := range blocks {
		// Serialize and deserialize to map config.ZKBlock to types.ZKBlock
		bBytes, _ := json.Marshal(b)
		var tBlock types.ZKBlock
		if json.Unmarshal(bBytes, &tBlock) == nil {
			ptrs = append(ptrs, &tBlock)
		}
	}

	return ptrs, nil
}

// Time Complexity: O(N) where N is the batch size
func (i *dbBlockIterator) Prev() ([]*types.ZKBlock, error) {
	if i.tailDone || i.tail < i.start {
		return nil, nil // Done
	}

	batchStart := uint64(0)
	if i.tail >= i.batchsize {
		batchStart = i.tail - i.batchsize + 1
	}
	if batchStart < i.start {
		batchStart = i.start
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, err
	}

	blocks, err := DB_OPs.GetBlocksRange(conn, batchStart, i.tail)
	if err != nil {
		return nil, err
	}

	if batchStart <= i.start {
		i.tailDone = true
	} else {
		i.tail = batchStart - 1
	}

	var ptrs []*types.ZKBlock
	for _, b := range blocks {
		bBytes, _ := json.Marshal(b)
		var tBlock types.ZKBlock
		if json.Unmarshal(bBytes, &tBlock) == nil {
			ptrs = append(ptrs, &tBlock)
		}
	}

	for left, right := 0, len(ptrs)-1; left < right; left, right = left+1, right-1 {
		ptrs[left], ptrs[right] = ptrs[right], ptrs[left]
	}

	return ptrs, nil
}

// Time Complexity: O(1)
func (i *dbBlockIterator) Close() {
	i.current = 0
	i.tail = 0
	i.start = 0
	i.end = 0
	i.batchsize = 0
}
