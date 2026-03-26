package NodeInfo

import (
	"context"
	"math/big"
	"time"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/config"
	"gossipnode/DB_OPs"
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

	for _, nh := range data {
		if nh == nil {
			continue
		}

		// FastSync splits blocks into Headers and NonHeaders. During WriteData, the block header usually exists already in DB.
		// We fetch it, append non-headers, and overwrite. DB_OPs.StoreZKBlock uses SafeCreate with KV serialization.
		b, err := DB_OPs.GetZKBlockByNumber(conn, nh.BlockNumber)
		if err != nil {
			// If not found, create a new Empty block and populate it to satisfy ImmuDB StoreZKBlock constraints
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
		}

		var txs []config.Transaction
		for _, dbTx := range nh.Transactions {
			tx := dbTx.Tx
			if tx == nil {
				continue
			}

			cfgTx := config.Transaction{
				Type:     uint8(tx.Type),
				GasLimit: tx.GasLimit,
				Data:     tx.Data,
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
			if len(tx.V) > 0 {
				cfgTx.V = new(big.Int).SetBytes(tx.V)
			}
			if len(tx.R) > 0 {
				cfgTx.R = new(big.Int).SetBytes(tx.R)
			}
			if len(tx.S) > 0 {
				cfgTx.S = new(big.Int).SetBytes(tx.S)
			}

			txs = append(txs, cfgTx)
		}

		// Reattach new transactions
		if len(txs) > 0 {
			b.Transactions = txs
		}

		err = DB_OPs.StoreZKBlock(conn, b)
		if err != nil {
			return err
		}
	}

	return nil
}
