package NodeInfo

import (
	"fmt"
	"math/big"
	"strings"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
)

type DataWriter struct{}

func (sync *sync_struct) NewDataWriter() types.WriteData {
	return &DataWriter{}
}

func (dw *DataWriter) WriteData(data []*blockpb.NonHeaders) error {
	if len(data) == 0 {
		return nil
	}

	for _, nh := range data {
		if nh == nil {
			continue
		}

		b, err := DB_OPs.GetZKBlockByNumber(nil, nh.BlockNumber)
		if err != nil {
			b = &config.ZKBlock{BlockNumber: nh.BlockNumber}
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
					at := config.AccessTuple{Address: common.BytesToAddress(pbAT.Address)}
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
			txs = append(txs, cfgTx)
		}

		if len(txs) > 0 {
			b.Transactions = txs
		}

		if err := DB_OPs.StoreZKBlock(nil, b); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("WriteData: block %d: %w", b.BlockNumber, err)
			}
			// Block already exists — StoreZKBlock upserts, so this shouldn't happen
			// with the new SQL backend. Log and continue.
		}
	}
	return nil
}

// bytesToCommitment lives in thebe_block_nonheaders.go (little-endian pair with commitmentToBytes).
