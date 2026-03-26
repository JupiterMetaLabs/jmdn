package NodeInfo

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/config"
	"gossipnode/DB_OPs"
)

type HeadersWriter struct{}

// Time Complexity: O(1)
func (sync *sync_struct) NewHeadersWriter() types.WriteHeaders {
	return &HeadersWriter{}
}

// Time Complexity: O(N) where N is the number of headers
func (hw *HeadersWriter) WriteHeaders(headers []*block.Header) error {
	if len(headers) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return err
	}

	for _, h := range headers {
		b := &config.ZKBlock{
			BlockNumber: h.BlockNumber,
			ProofHash:   h.ProofHash,
			Timestamp:   h.Timestamp,
			Status:      h.Status,
			TxnsRoot:    h.TxnsRoot,
			ExtraData:   h.ExtraData,
			GasLimit:    h.GasLimit,
			GasUsed:      h.GasUsed,
		}
		
		if len(h.StateRoot) > 0 {
			b.StateRoot = common.BytesToHash(h.StateRoot)
		}
		if len(h.BlockHash) > 0 {
			b.BlockHash = common.BytesToHash(h.BlockHash)
		}
		if len(h.PrevHash) > 0 {
			b.PrevHash = common.BytesToHash(h.PrevHash)
		}
		if len(h.CoinbaseAddr) > 0 {
			addr := common.BytesToAddress(h.CoinbaseAddr)
			b.CoinbaseAddr = &addr
		}
		if len(h.ZkvmAddr) > 0 {
			addr := common.BytesToAddress(h.ZkvmAddr)
			b.ZKVMAddr = &addr
		}
		
		err := DB_OPs.StoreZKBlock(conn, b)
		if err != nil {
			return err
		}
	}
	
	return nil
}

