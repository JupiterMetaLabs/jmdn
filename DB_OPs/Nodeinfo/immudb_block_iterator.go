package NodeInfo

import (
	"context"
	"time"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// configToFastsyncBlock converts a config.ZKBlock to a fastsync_types.ZKBlock
// via direct field assignment. Both structs are field-identical; this replaces
// the previous JSON marshal/unmarshal round-trip which silently zeroed any field
// whose JSON tag or type didn't survive the cycle (Medium finding M2 in the
// security audit — commit: fix(iterator): replace JSON round-trip with direct conversion).
func configToFastsyncBlock(b *config.ZKBlock) *fastsync_types.ZKBlock {
	out := &fastsync_types.ZKBlock{
		StarkProof:   b.StarkProof,
		Commitment:   b.Commitment,
		ProofHash:    b.ProofHash,
		Status:       b.Status,
		TxnsRoot:     b.TxnsRoot,
		Timestamp:    b.Timestamp,
		ExtraData:    b.ExtraData,
		StateRoot:    b.StateRoot,
		LogsBloom:    b.LogsBloom,
		CoinbaseAddr: b.CoinbaseAddr,
		ZKVMAddr:     b.ZKVMAddr,
		PrevHash:     b.PrevHash,
		BlockHash:    b.BlockHash,
		GasLimit:     b.GasLimit,
		GasUsed:      b.GasUsed,
		BlockNumber:  b.BlockNumber,
	}

	if len(b.Transactions) > 0 {
		out.Transactions = make([]fastsync_types.Transaction, len(b.Transactions))
		for i, tx := range b.Transactions {
			out.Transactions[i] = fastsync_types.Transaction{
				Hash:           tx.Hash,
				From:           tx.From,
				To:             tx.To,
				Value:          tx.Value,
				Type:           tx.Type,
				Timestamp:      tx.Timestamp,
				ChainID:        tx.ChainID,
				Nonce:          tx.Nonce,
				GasLimit:       tx.GasLimit,
				GasPrice:       tx.GasPrice,
				MaxFee:         tx.MaxFee,
				MaxPriorityFee: tx.MaxPriorityFee,
				Data:           tx.Data,
				V:              tx.V,
				R:              tx.R,
				S:              tx.S,
			}
			if len(tx.AccessList) > 0 {
				out.Transactions[i].AccessList = make(fastsync_types.AccessList, len(tx.AccessList))
				for j, at := range tx.AccessList {
					out.Transactions[i].AccessList[j] = fastsync_types.AccessTuple{
						Address:     at.Address,
						StorageKeys: at.StorageKeys,
					}
				}
			}
		}
	}

	return out
}

type dbBlockIterator struct {
	current   uint64
	tail      uint64
	start     uint64
	end       uint64
	batchsize uint64
	tailDone  bool
}

// Time Complexity: O(1)
func (sync *sync_struct) NewBlockIterator(start, end uint64, batchsize int) fastsync_types.BlockIterator {
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
func (i *dbBlockIterator) Next() ([]*fastsync_types.ZKBlock, error) {
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

	batchStart := i.current
	i.current = batchEnd + 1

	// Build a lookup map so we can detect missing positions.
	// GetBlocksRange / GetAll silently drops entries not found in ImmuDB —
	// it returns fewer items than the requested range, with no nil sentinel.
	// Without this compensation, block N+1's hash would occupy tree position N
	// for every gap, silently corrupting the Merkle root.
	blockMap := make(map[uint64]*config.ZKBlock, len(blocks))
	for _, b := range blocks {
		if b != nil {
			blockMap[b.BlockNumber] = b
		}
	}

	// Produce a positionally-correct slice: ptrs[k] = block (batchStart + k).
	// Missing positions remain nil — builder.go substitutes a zero-hash leaf,
	// preserving the position invariant required by the JMDN_Merkletree library.
	count := batchEnd - batchStart + 1
	ptrs := make([]*fastsync_types.ZKBlock, count)
	for pos := uint64(0); pos < count; pos++ {
		b, ok := blockMap[batchStart+pos]
		if !ok {
			// nil → caller (builder.go) inserts zero-hash for this position
			continue
		}
		ptrs[pos] = configToFastsyncBlock(b)
	}

	return ptrs, nil
}

// Time Complexity: O(N) where N is the batch size
func (i *dbBlockIterator) Prev() ([]*fastsync_types.ZKBlock, error) {
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

	// Capture the batch end before i.tail is updated below.
	batchEnd := i.tail

	blocks, err := DB_OPs.GetBlocksRange(conn, batchStart, batchEnd)
	if err != nil {
		return nil, err
	}

	if batchStart <= i.start {
		i.tailDone = true
	} else {
		i.tail = batchStart - 1
	}

	// Same gap-compensation as Next(): build a position map so missing blocks
	// produce nil entries rather than silently shifting subsequent hashes.
	blockMap := make(map[uint64]*config.ZKBlock, len(blocks))
	for _, b := range blocks {
		if b != nil {
			blockMap[b.BlockNumber] = b
		}
	}

	count := batchEnd - batchStart + 1
	ptrs := make([]*fastsync_types.ZKBlock, count)
	for pos := uint64(0); pos < count; pos++ {
		b, ok := blockMap[batchStart+pos]
		if !ok {
			continue
		}
		ptrs[pos] = configToFastsyncBlock(b)
	}

	// Prev() is a reverse iterator — return blocks in descending order.
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
