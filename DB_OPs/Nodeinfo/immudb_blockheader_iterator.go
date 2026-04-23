package NodeInfo

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"gossipnode/DB_OPs"
)

type dbBlockHeaderIterator struct{}

// Time Complexity: O(1)
func (sync *sync_struct) NewBlockHeaderIterator() types.BlockHeader {
	return &dbBlockHeaderIterator{}
}

// Time Complexity: O(N) where N is the number of block headers requested
func (i *dbBlockHeaderIterator) GetBlockHeaders(blocknumbers []uint64) ([]*block.Header, error) {
	var headers []*block.Header

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, err
	}

	for _, num := range blocknumbers {
		b, err := DB_OPs.GetZKBlockByNumber(conn, num)
		if err != nil || b == nil {
			continue
		}
		
		h := &block.Header{
			ProofHash:    b.ProofHash,
			Status:       b.Status,
			TxnsRoot:     b.TxnsRoot,
			Timestamp:    b.Timestamp,
			ExtraData:    b.ExtraData,
			StateRoot:    b.StateRoot[:],
			BlockHash:    b.BlockHash[:],
			PrevHash:     b.PrevHash[:],
			GasLimit:     b.GasLimit,
			GasUsed:      b.GasUsed,
			BlockNumber:  b.BlockNumber,
		}
		if b.CoinbaseAddr != nil {
			h.CoinbaseAddr = b.CoinbaseAddr[:]
		}
		if b.ZKVMAddr != nil {
			h.ZkvmAddr = b.ZKVMAddr[:]
		}

		headers = append(headers, h)
	}

	return headers, nil
}

// Time Complexity: O(N) where N is the end - start range
func (i *dbBlockHeaderIterator) GetBlockHeadersRange(start, end uint64) ([]*block.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, err
	}

	blocks, err := DB_OPs.GetBlocksRange(conn, start, end)
	if err != nil {
		return nil, err
	}

	var headers []*block.Header
	for _, b := range blocks {
		h := &block.Header{
			ProofHash:    b.ProofHash,
			Status:       b.Status,
			TxnsRoot:     b.TxnsRoot,
			Timestamp:    b.Timestamp,
			ExtraData:    b.ExtraData,
			StateRoot:    b.StateRoot[:],
			BlockHash:    b.BlockHash[:],
			PrevHash:     b.PrevHash[:],
			GasLimit:     b.GasLimit,
			GasUsed:      b.GasUsed,
			BlockNumber:  b.BlockNumber,
		}
		if b.CoinbaseAddr != nil {
			h.CoinbaseAddr = b.CoinbaseAddr[:]
		}
		if b.ZKVMAddr != nil {
			h.ZkvmAddr = b.ZKVMAddr[:]
		}
		headers = append(headers, h)
	}
	return headers, nil
}
