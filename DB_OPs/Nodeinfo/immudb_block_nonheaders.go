package NodeInfo

import (
	"context"
	"encoding/binary"
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
			CreatedAt: b.Timestamp,
		},
	}

	if b.ProofHash != "" {
		nh.ZkProof = &blockpb.ZKProof{
			ProofHash:  b.ProofHash,
			StarkProof: b.StarkProof,
			Commitment: commitmentToBytes(b.Commitment),
		}
	}

	for idx, tx := range b.Transactions {
		pbTx := &blockpb.Transaction{
			Hash:     tx.Hash[:],
			Type:     uint32(tx.Type),
			Nonce:    tx.Nonce,
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
		if tx.GasPrice != nil {
			pbTx.GasPrice = tx.GasPrice.Bytes()
		}
		if tx.MaxFee != nil {
			pbTx.MaxFee = tx.MaxFee.Bytes()
		}
		if tx.MaxPriorityFee != nil {
			pbTx.MaxPriorityFee = tx.MaxPriorityFee.Bytes()
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
			CreatedAt: b.Timestamp,
		})
	}
	return nh
}

// commitmentToBytes encodes a []uint32 commitment to raw bytes (4 bytes per element, little-endian).
// This matches the block_nonheader.proto ZKProof.commitment (bytes) field.
func commitmentToBytes(c []uint32) []byte {
	if len(c) == 0 {
		return nil
	}
	buf := make([]byte, len(c)*4)
	for i, v := range c {
		binary.LittleEndian.PutUint32(buf[i*4:], v)
	}
	return buf
}

// bytesToCommitment decodes raw bytes back to []uint32 (4 bytes per element, little-endian).
func bytesToCommitment(b []byte) []uint32 {
	if len(b) == 0 {
		return nil
	}
	count := len(b) / 4
	result := make([]uint32, count)
	for i := 0; i < count; i++ {
		result[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return result
}
