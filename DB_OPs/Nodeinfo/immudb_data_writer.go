package NodeInfo

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
)

type DataWriter struct{}

// Time Complexity: O(1)
func (sync *sync_struct) NewDataWriter() types.WriteData {
	return &DataWriter{}
}

// Time Complexity: O(N*M) where N is number of NonHeaders and M is transactions per batch
func (dw *DataWriter) WriteData(data []*blockpb.NonHeaders) error {
	if len(data) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return err
	}

	var highestWritten uint64
	var didWriteBlock bool // tracks whether any block was successfully stored

	for _, nh := range data {
		if nh == nil {
			continue
		}

		// FastSync splits blocks into Headers and NonHeaders. During WriteData, the block header
		// usually exists already in DB from WriteHeaders. We fetch it, merge non-header data, and overwrite.
		b, err := DB_OPs.GetZKBlockByNumber(conn, nh.BlockNumber)
		if err != nil {
			// Block header not yet written — create a minimal block to attach non-header data.
			b = &config.ZKBlock{
				BlockNumber: nh.BlockNumber,
			}
			if nh.Snapshot != nil && len(nh.Snapshot.BlockHash) > 0 {
				b.BlockHash = common.BytesToHash(nh.Snapshot.BlockHash)
			}
		}

		if nh.ZkProof != nil {
			b.ProofHash = nh.ZkProof.ProofHash
			b.StarkProof = nh.ZkProof.StarkProof
			b.Commitment = bytesToCommitment(nh.ZkProof.Commitment)
		}

		var txs []config.Transaction
		for _, dbTx := range nh.Transactions {
			tx := dbTx.Tx
			if tx == nil {
				continue
			}

			cfgTx := config.Transaction{
				Type:      uint8(tx.Type),
				Timestamp: tx.Timestamp,
				Nonce:     tx.Nonce,
				GasLimit:  tx.GasLimit,
				Data:      tx.Data,
			}

			if len(tx.Hash) > 0 {
				cfgTx.Hash = common.BytesToHash(tx.Hash)
			}
			if len(tx.From) > 0 {
				addr := common.BytesToAddress(tx.From)
				cfgTx.From = &addr
			}
			if len(tx.To) > 0 {
				addr := common.BytesToAddress(tx.To)
				cfgTx.To = &addr
			}
			if len(tx.Value) > 0 {
				cfgTx.Value = new(big.Int).SetBytes(tx.Value)
			}
			if len(tx.ChainId) > 0 {
				cfgTx.ChainID = new(big.Int).SetBytes(tx.ChainId)
			}
			if len(tx.GasPrice) > 0 {
				cfgTx.GasPrice = new(big.Int).SetBytes(tx.GasPrice)
			}
			if len(tx.MaxFee) > 0 {
				cfgTx.MaxFee = new(big.Int).SetBytes(tx.MaxFee)
			}
			if len(tx.MaxPriorityFee) > 0 {
				cfgTx.MaxPriorityFee = new(big.Int).SetBytes(tx.MaxPriorityFee)
			}
			if len(tx.AccessList) > 0 {
				cfgTx.AccessList = make(config.AccessList, 0, len(tx.AccessList))
				for _, pbAT := range tx.AccessList {
					at := config.AccessTuple{
						Address: common.BytesToAddress(pbAT.Address),
					}
					for _, sk := range pbAT.StorageKeys {
						at.StorageKeys = append(at.StorageKeys, common.BytesToHash(sk))
					}
					cfgTx.AccessList = append(cfgTx.AccessList, at)
				}
			}
			if len(tx.V) > 0 {
				cfgTx.V = new(big.Int).SetBytes(tx.V)
			}
			if len(tx.R) > 0 {
				cfgTx.R = new(big.Int).SetBytes(tx.R)
			}
			if len(tx.S) > 0 {
				cfgTx.S = new(big.Int).SetBytes(tx.S)
			}
			// ChainID and AccessList are fully handled in the pass above.
			// No further processing needed here.

			txs = append(txs, cfgTx)
		}

		// Always overwrite Transactions from the DataSync response.
		// The previous guard (if len(txs) > 0) was wrong: if the server sends
		// transactions for this block, they must be written; if it sends none,
		// the block genuinely has no transactions and we must clear any stale
		// data left by PubSub/HeaderSync skeleton writes.
		b.Transactions = txs

		if err := DB_OPs.StoreZKBlock(conn, b); err != nil {
			// if err not nil, then force write or update
			if strings.Contains(err.Error(), "already exists") {
				blockKey := fmt.Sprintf("%s%d", DB_OPs.PREFIX_BLOCK, b.BlockNumber)
				if err2 := DB_OPs.Update(blockKey, b); err2 != nil {
					return fmt.Errorf("force update block %d failed: %w", b.BlockNumber, err2)
				}

				hashKey := fmt.Sprintf("%s%s", DB_OPs.PREFIX_BLOCK_HASH, b.BlockHash.Hex())
				if err2 := DB_OPs.Update(hashKey, blockKey); err2 != nil {
					return fmt.Errorf("force update hash mapping failed: %w", err2)
				}

				// Write tx:<hash> → blockNumber index for each transaction.
				// WriteHeaders stores blocks without transactions, so StoreZKBlock's tx
				// indexing loop runs 0 times there. This is the only place those index
				// entries get written for existing blocks — required for GetTransactionByHash.
				for _, tx := range b.Transactions {
					txKey := fmt.Sprintf("%s%s", DB_OPs.DEFAULT_PREFIX_TX, tx.Hash)
					if err2 := DB_OPs.Create(conn, txKey, b.BlockNumber); err2 != nil {
						if !strings.Contains(err2.Error(), "already exists") {
							return fmt.Errorf("store tx index for %s: %w", tx.Hash, err2)
						}
					}
				}
			} else {
				return err
			}
		}

		if !didWriteBlock || b.BlockNumber > highestWritten {
			highestWritten = b.BlockNumber
			didWriteBlock = true
		}
	}

	// Update latest_block once to the highest block number written in this batch.
	// Per-block updates (done inside the loop above) are non-deterministic when
	// DataSync workers run concurrently — the last worker to finish may not hold
	// the highest block. A single update at the end is authoritative.
	//
	// The previous guard was `highestWritten > 0`, which silently skipped the
	// update when a batch contained only block 0 (genesis). Using didWriteBlock
	// correctly handles genesis — latest_block is always set after any data write.
	if didWriteBlock {
		if err2 := DB_OPs.Update("latest_block", highestWritten); err2 != nil {
			return fmt.Errorf("update latest_block to %d failed: %w", highestWritten, err2)
		}
		// Fix 2: record write time so SyncMonitor propagation guard can skip a
		// Merkle check that races with a block that just landed.
		notifyBlockReceived()
	}

	return nil
}
