package NodeInfo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
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

	// NOTE: the latest_block snapshot/restore dance that used to live here is
	// GONE. It existed to undo StoreZKBlock's per-block marker write for
	// skeleton blocks — but the restore raced concurrent DataSync workers and
	// live processing, clobbering their legitimate advances back to a stale
	// value (a regression vector built to patch another). StoreZKBlock no
	// longer touches the marker, so skeleton writes cannot advance it and
	// there is nothing to restore.

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

	// Fix 2: record that blocks arrived so SyncMonitor propagation guard
	// can skip a Merkle check racing with an in-flight header write.
	notifyBlockReceived()

	// Update header_latest_block so SyncConfirmation can build the correct Merkle
	// range. This is separate from latest_block (which DataSync owns) so the
	// explorer still shows only fully data-synced blocks.
	if len(headers) > 0 {
		highestWritten := headers[0].BlockNumber
		for _, h := range headers[1:] {
			if h.BlockNumber > highestWritten {
				highestWritten = h.BlockNumber
			}
		}
		if err2 := DB_OPs.Update("header_latest_block", highestWritten); err2 != nil {
			return fmt.Errorf("update header_latest_block failed: %w", err2)
		}
	}

	// No latest_block restore here — see the note at the top of this function.
	return nil
}
