package NodeInfo

import (
	"fmt"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

type HeadersWriter struct{}

func (sync *sync_struct) NewHeadersWriter() types.WriteHeaders {
	return &HeadersWriter{}
}

func (hw *HeadersWriter) WriteHeaders(headers []*block.Header) error {
	if len(headers) == 0 {
		return nil
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
			GasUsed:     h.GasUsed,
			LogsBloom:   h.LogsBloom,
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

		if err := DB_OPs.StoreZKBlock(nil, b); err != nil {
			return fmt.Errorf("WriteHeaders: block %d: %w", b.BlockNumber, err)
		}
	}
	// Record that blocks arrived so the SyncMonitor propagation guard can skip
	// a Merkle check racing with an in-flight header write (main Fix 2).
	notifyBlockReceived()

	// "header_latest_block" sentinel is now derived from SQL MAX(block_number) —
	// DB_OPs.Update is a no-op for that key.
	return nil
}
