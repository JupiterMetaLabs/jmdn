// MODULE: DB_OPs/thebe_gateway_adapter.go
// PURPOSE: Bridge ThebeShadowWriter (ImmuDB-style interface) to ThebeGateway (typed domain writes).
//          Called from SetThebeShadowWriter() after each ImmuDB block write.
//
// CORE DATA STRUCTURES:
//   - GatewayAdapter: holds ThebeGateway (interface). Stateless per-call.
//
// TO MODIFY BEHAVIOR:
//   - Change block→record mapping: edit blockToRecord() in this file
//   - Change tx→record mapping: edit txToRecord() in this file
//
// DO NOT:
//   - Import gossipnode/DB_OPs/dualdb or gossipnode/DB_OPs/cassata
//   - Use the config.PooledConnection arg — it is ignored (gateway uses its own connections)
//
// EXTENSION POINT: if ThebeShadowWriter gains new methods, implement them here

package DB_OPs

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// GatewayAdapter implements ThebeShadowWriter using ThebeGateway.
type GatewayAdapter struct {
	gw thebegateway.ThebeGateway
}

// NewGatewayAdapter constructs a GatewayAdapter wrapping the given ThebeGateway.
func NewGatewayAdapter(gw thebegateway.ThebeGateway) *GatewayAdapter {
	return &GatewayAdapter{gw: gw}
}

// Compile-time interface check.
var _ ThebeShadowWriter = (*GatewayAdapter)(nil)

// StoreZKBlock implements ThebeShadowWriter. Fans out one ZKBlock write into
// WriteBlock + WriteSnapshot + WriteZKProof + WriteTransaction×N calls.
// The mainDBClient arg is unused — ThebeGateway manages its own connections.
// Time: O(n) where n = number of transactions in the block
func (a *GatewayAdapter) StoreZKBlock(_ *config.PooledConnection, block *config.ZKBlock) error {
	ctx := context.Background()

	if err := a.gw.WriteBlock(ctx, blockToRecord(block)); err != nil {
		return fmt.Errorf("GatewayAdapter.StoreZKBlock: write block %d: %w", block.BlockNumber, err)
	}
	if err := a.gw.WriteSnapshot(ctx, snapshotToRecord(block)); err != nil {
		return fmt.Errorf("GatewayAdapter.StoreZKBlock: write snapshot %d: %w", block.BlockNumber, err)
	}
	if err := a.gw.WriteZKProof(ctx, zkProofToRecord(block)); err != nil {
		return fmt.Errorf("GatewayAdapter.StoreZKBlock: write zk proof %d: %w", block.BlockNumber, err)
	}
	for i := range block.Transactions {
		rec := txToRecord(&block.Transactions[i], block.BlockNumber, i)
		if err := a.gw.WriteTransaction(ctx, rec); err != nil {
			return fmt.Errorf("GatewayAdapter.StoreZKBlock: write tx %s: %w", rec.TxHash, err)
		}
	}
	return nil
}

// blockToRecord maps a ZKBlock to a BlockRecord DTO.
func blockToRecord(block *config.ZKBlock) *thebegateway.BlockRecord {
	var coinbase, zkvm string
	if block.CoinbaseAddr != nil {
		coinbase = block.CoinbaseAddr.Hex()
	}
	if block.ZKVMAddr != nil {
		zkvm = block.ZKVMAddr.Hex()
	}
	return &thebegateway.BlockRecord{
		BlockNumber:  block.BlockNumber,
		BlockHash:    block.BlockHash.Hex(),
		ParentHash:   block.PrevHash.Hex(),
		Timestamp:    time.Unix(block.Timestamp, 0).UTC(),
		TxsRoot:      block.TxnsRoot,
		StateRoot:    block.StateRoot.Hex(),
		LogsBloom:    block.LogsBloom,
		CoinbaseAddr: coinbase,
		ZKVMAddr:     zkvm,
		GasLimit:     block.GasLimit,
		GasUsed:      block.GasUsed,
		Status:       0,
		ExtraData:    map[string]any{"extra_data": block.ExtraData},
	}
}

// snapshotToRecord maps a ZKBlock to a SnapshotRecord DTO.
func snapshotToRecord(block *config.ZKBlock) *thebegateway.SnapshotRecord {
	return &thebegateway.SnapshotRecord{
		BlockNumber: block.BlockNumber,
		BlockHash:   block.BlockHash.Hex(),
		CreatedAt:   time.Unix(block.Timestamp, 0).UTC(),
	}
}

// zkProofToRecord maps a ZKBlock to a ZKProofRecord DTO.
// Commitment []uint32 → []byte (big-endian 4 bytes per element)
func zkProofToRecord(block *config.ZKBlock) *thebegateway.ZKProofRecord {
	commitBytes := make([]byte, len(block.Commitment)*4)
	for i, v := range block.Commitment {
		binary.BigEndian.PutUint32(commitBytes[i*4:], v)
	}
	return &thebegateway.ZKProofRecord{
		BlockNumber: block.BlockNumber,
		ProofHash:   block.ProofHash,
		StarkProof:  block.StarkProof,
		Commitment:  commitBytes,
	}
}

// txToRecord maps a Transaction + block context to a TransactionRecord DTO.
func txToRecord(tx *config.Transaction, blockNumber uint64, txIndex int) *thebegateway.TransactionRecord {
	var fromAddr string
	if tx.From != nil {
		fromAddr = tx.From.Hex()
	}

	var toAddr *string
	if tx.To != nil {
		s := tx.To.Hex()
		toAddr = &s
	}

	return &thebegateway.TransactionRecord{
		TxHash:            tx.Hash.Hex(),
		BlockNumber:       blockNumber,
		TxIndex:           int16(txIndex),
		FromAddr:          fromAddr,
		ToAddr:            toAddr,
		ValueWei:          bigIntStr(tx.Value),
		Nonce:             strconv.FormatUint(tx.Nonce, 10),
		Type:              int16(tx.Type),
		GasLimit:          strconv.FormatUint(tx.GasLimit, 10),
		GasPriceWei:       bigIntStr(tx.GasPrice),
		MaxFeeWei:         bigIntStr(tx.MaxFee),
		MaxPriorityFeeWei: bigIntStr(tx.MaxPriorityFee),
		SigV:              bigIntUint64(tx.V),
		SigR:              bigIntHex64(tx.R),
		SigS:              bigIntHex64(tx.S),
	}
}

func bigIntStr(b *big.Int) string {
	if b == nil {
		return "0"
	}
	return b.String()
}

func bigIntUint64(b *big.Int) uint64 {
	if b == nil {
		return 0
	}
	return b.Uint64()
}

// bigIntHex64 formats a *big.Int as a 0x-prefixed 64-char hex string (CHAR(66)).
func bigIntHex64(b *big.Int) string {
	if b == nil {
		return "0x" + strings.Repeat("0", 64)
	}
	return fmt.Sprintf("0x%064x", b)
}
