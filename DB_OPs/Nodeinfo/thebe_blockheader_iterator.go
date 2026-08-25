package NodeInfo

import (
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"

	"gossipnode/DB_OPs"
)

type dbBlockHeaderIterator struct{}

// Time Complexity: O(1)
// __DEAD_CODE_AUDIT_PUBLIC__
func (sync *sync_struct) NewBlockHeaderIterator() types.BlockHeader {
	return &dbBlockHeaderIterator{}
}

// Time Complexity: O(N) where N is the number of block headers requested
// __DEAD_CODE_AUDIT_PUBLIC__
func (i *dbBlockHeaderIterator) GetBlockHeaders(blocknumbers []uint64) ([]*block.Header, error) {
	var headers []*block.Header

	for _, num := range blocknumbers {
		b, err := DB_OPs.GetZKBlockByNumber(nil, num)
		if err != nil || b == nil {
			continue
		}

		h := &block.Header{
			ProofHash:   b.ProofHash,
			Status:      b.Status,
			TxnsRoot:    b.TxnsRoot,
			Timestamp:   b.Timestamp,
			ExtraData:   b.ExtraData,
			StateRoot:   b.StateRoot[:],
			BlockHash:   b.BlockHash[:],
			PrevHash:    b.PrevHash[:],
			GasLimit:    b.GasLimit,
			GasUsed:     b.GasUsed,
			BlockNumber: b.BlockNumber,
			LogsBloom:   b.LogsBloom,
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
// __DEAD_CODE_AUDIT_PUBLIC__
func (i *dbBlockHeaderIterator) GetBlockHeadersRange(start, end uint64) ([]*block.Header, error) {
	blocks, err := DB_OPs.GetBlocksRange(nil, start, end)
	if err != nil {
		return nil, err
	}

	var headers []*block.Header
	for _, b := range blocks {
		h := &block.Header{
			ProofHash:   b.ProofHash,
			Status:      b.Status,
			TxnsRoot:    b.TxnsRoot,
			Timestamp:   b.Timestamp,
			ExtraData:   b.ExtraData,
			StateRoot:   b.StateRoot[:],
			BlockHash:   b.BlockHash[:],
			PrevHash:    b.PrevHash[:],
			GasLimit:    b.GasLimit,
			GasUsed:     b.GasUsed,
			BlockNumber: b.BlockNumber,
			LogsBloom:   b.LogsBloom,
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
