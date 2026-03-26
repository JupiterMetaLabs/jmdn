package NodeInfo

import (
	"context"
	"time"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"gossipnode/DB_OPs"
	"gossipnode/config"
)

type dbBlockNonHeaderIterator struct{}

// Time Complexity: O(1)
func (sync *sync_struct) NewBlockNonHeaderIterator() types.BlockNonHeader {
	return &dbBlockNonHeaderIterator{}
}

// Time Complexity: O(N*M) where N is blocknumbers length and M is transactions per block
func (i *dbBlockNonHeaderIterator) GetBlockNonHeaders(blocknumbers []uint64) ([]*blockpb.NonHeaders, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, err
	}

	var results []*blockpb.NonHeaders
	for _, num := range blocknumbers {
		b, err := DB_OPs.GetZKBlockByNumber(conn, num)
		if err != nil || b == nil {
			continue
		}
		results = append(results, convertZKBlockToNonHeaders(b))
	}
	return results, nil
}

// Time Complexity: O(N*M) where N is end-start range and M is transactions per block
func (i *dbBlockNonHeaderIterator) GetBlockNonHeadersRange(start, end uint64) ([]*blockpb.NonHeaders, error) {
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

	var results []*blockpb.NonHeaders
	for _, b := range blocks {
		results = append(results, convertZKBlockToNonHeaders(b))
	}
	return results, nil
}

func convertZKBlockToNonHeaders(b *config.ZKBlock) *blockpb.NonHeaders {
	nh := &blockpb.NonHeaders{
		BlockNumber: b.BlockNumber,
		Snapshot: &blockpb.SnapshotRecord{
			BlockHash: b.BlockHash[:],
			CreatedAt: time.Now().UnixNano(),
		},
	}

	if b.ProofHash != "" {
		nh.ZkProof = &blockpb.ZKProof{
			ProofHash:  b.ProofHash,
			StarkProof: b.StarkProof,
			Commitment: nil, // If applicable, marshall b.Commitment
		}
	}

	// L1Finality conversion - DB_OPs usually syncs this independently or as part of block
	nh.L1Finality = &blockpb.L1Finality{} 

	for idx, tx := range b.Transactions {
		pbTx := &blockpb.Transaction{
			Hash:     tx.Hash[:],
			Type:     uint32(tx.Type),
			GasLimit: tx.GasLimit,
			Data:     tx.Data,
		}
		if tx.From != nil {
			pbTx.From = tx.From[:]
		}
		if tx.To != nil {
			pbTx.To = tx.To[:]
		}
		if tx.Value != nil {
			pbTx.Value = tx.Value.Bytes()
		}
		if tx.V != nil {
			pbTx.V = tx.V.Bytes()
		}
		if tx.R != nil {
			pbTx.R = tx.R.Bytes()
		}
		if tx.S != nil {
			pbTx.S = tx.S.Bytes()
		}
		
		nh.Transactions = append(nh.Transactions, &blockpb.DBTransaction{
			Tx:        pbTx,
			TxIndex:   uint32(idx),
			CreatedAt: time.Now().UnixNano(),
		})
	}
	return nh
}
