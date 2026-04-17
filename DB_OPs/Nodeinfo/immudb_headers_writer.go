package NodeInfo

import (
	"context"
	"fmt"
	"strings"
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

	// Snapshot latest_block before writing any headers.
	// HeaderSync writes skeleton blocks (no transactions) so it must not advance
	// the latest_block marker — that would make the explorer and StartupSync think
	// the node is fully synced up to the last header, when DataSync hasn't run yet.
	// We restore this value after all headers are written.
	prevLatest, prevErr := DB_OPs.GetLatestBlockNumber(conn)

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
			if strings.Contains(err.Error(), "already exists") {
				blockKey := fmt.Sprintf("%s%d", DB_OPs.PREFIX_BLOCK, b.BlockNumber)
				if err2 := DB_OPs.Update(blockKey, b); err2 != nil {
					return fmt.Errorf("force update block %d failed: %w", b.BlockNumber, err2)
				}

				hashKey := fmt.Sprintf("%s%s", DB_OPs.PREFIX_BLOCK_HASH, b.BlockHash.Hex())
				if err2 := DB_OPs.Update(hashKey, blockKey); err2 != nil {
					return fmt.Errorf("force update hash mapping failed: %w", err2)
				}

				// Do NOT update latest_block here — DataSync owns the marker.
			} else {
				return err
			}
		}
	}

	// Restore latest_block to the pre-HeaderSync value so the marker always
	// reflects the last fully data-synced block, not just the last header.
	if prevErr == nil {
		if err2 := DB_OPs.Update("latest_block", prevLatest); err2 != nil {
			return fmt.Errorf("restore latest_block after HeaderSync failed: %w", err2)
		}
	}

	return nil
}

