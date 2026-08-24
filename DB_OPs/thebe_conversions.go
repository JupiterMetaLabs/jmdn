package DB_OPs

// thebe_conversions.go — conversions between thebegateway record types and config domain types.
// Used by the ThebeHandle-based reimplementations of immuclient.go and account_immuclient.go.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// blockRecordToZKBlock converts a thebegateway.BlockRecord back to a config.ZKBlock.
// Transaction slices are NOT populated here (use GetTransactionsByBlock separately).
func blockRecordToZKBlock(r *thebegateway.BlockRecord) (*config.ZKBlock, error) {
	if r == nil {
		return nil, fmt.Errorf("blockRecordToZKBlock: nil record")
	}

	var coinbase *common.Address
	if r.CoinbaseAddr != "" {
		a := common.HexToAddress(r.CoinbaseAddr)
		coinbase = &a
	}
	var zkvm *common.Address
	if r.ZKVMAddr != "" {
		a := common.HexToAddress(r.ZKVMAddr)
		zkvm = &a
	}

	extraData := ""
	if ed, ok := r.ExtraData["extra_data"]; ok {
		if s, ok2 := ed.(string); ok2 {
			extraData = s
		}
	}

	// Slot/Period — persisted by DB_OPs/backend/block.go's toBlockRecord
	// (2026-08-24, docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8) so a
	// restarted node can recover its slot/epoch clock from its own committed
	// history (messaging.SlotStore.SeedFromCommittedTip). This was the
	// missing read-side half: the write side existed and was tested, but
	// nothing decoded these two keys back out of ExtraData into the ZKBlock
	// struct, so every caller of GetZKBlockByNumber saw Slot=0/Period=0 on a
	// real block regardless of what was actually persisted.
	blk := &config.ZKBlock{
		BlockNumber:  r.BlockNumber,
		BlockHash:    common.HexToHash(r.BlockHash),
		PrevHash:     common.HexToHash(r.ParentHash),
		Timestamp:    r.Timestamp.Unix(),
		TxnsRoot:     r.TxsRoot,
		StateRoot:    common.HexToHash(r.StateRoot),
		LogsBloom:    r.LogsBloom,
		CoinbaseAddr: coinbase,
		ZKVMAddr:     zkvm,
		GasLimit:     r.GasLimit,
		GasUsed:      r.GasUsed,
		ExtraData:    extraData,
		Transactions: []config.Transaction{},
	}
	if v, ok := r.ExtraData["slot"]; ok {
		blk.Slot = extraDataUint64(v)
	}
	if v, ok := r.ExtraData["period"]; ok {
		blk.Period = extraDataUint64(v)
	}
	return blk, nil
}

// extraDataUint64 decodes a uint64 that round-tripped through ExtraData's
// map[string]any (itself round-tripped through JSON — see
// thebegateway/reader.go's scanBlock, and the cache decorator in
// DB_OPs/store/cache/block.go, both of which json.Unmarshal into this map).
// JSON numbers decode to float64 by default, never uint64/int64 directly, so
// a plain type-assertion to uint64 always misses — this is the one correct
// place that fact needs handling, rather than every caller re-discovering it.
// Also accepts the narrower numeric types directly, for any future writer
// that bypasses JSON (e.g. an in-process test building the map by hand).
func extraDataUint64(v any) uint64 {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case uint64:
		return n
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case json.Number:
		u, err := strconv.ParseUint(n.String(), 10, 64)
		if err != nil {
			return 0
		}
		return u
	default:
		return 0
	}
}

// txRecordToTransaction converts a thebegateway.TransactionRecord to a config.Transaction.
func txRecordToTransaction(r *thebegateway.TransactionRecord) *config.Transaction {
	if r == nil {
		return nil
	}

	tx := &config.Transaction{
		Hash:  common.HexToHash(r.TxHash),
		Type:  uint8(r.Type),
		Nonce: func() uint64 { n, _ := strconv.ParseUint(r.Nonce, 10, 64); return n }(),
	}

	if r.FromAddr != "" {
		a := common.HexToAddress(r.FromAddr)
		tx.From = &a
	}
	if r.ToAddr != nil && *r.ToAddr != "" {
		a := common.HexToAddress(*r.ToAddr)
		tx.To = &a
	}

	if v, ok := new(big.Int).SetString(r.ValueWei, 10); ok {
		tx.Value = v
	}
	gasLimit, _ := strconv.ParseUint(r.GasLimit, 10, 64)
	tx.GasLimit = gasLimit

	if r.GasPriceWei != "" && r.GasPriceWei != "0" {
		if p, ok := new(big.Int).SetString(r.GasPriceWei, 10); ok {
			tx.GasPrice = p
		}
	}
	if r.MaxFeeWei != "" && r.MaxFeeWei != "0" {
		if p, ok := new(big.Int).SetString(r.MaxFeeWei, 10); ok {
			tx.MaxFee = p
		}
	}
	if r.MaxPriorityFeeWei != "" && r.MaxPriorityFeeWei != "0" {
		if p, ok := new(big.Int).SetString(r.MaxPriorityFeeWei, 10); ok {
			tx.MaxPriorityFee = p
		}
	}

	tx.V = new(big.Int).SetUint64(r.SigV)

	return tx
}

// zkProofRecordToZKBlock fills ZK proof fields on an existing ZKBlock from a ZKProofRecord.
func zkProofRecordToZKBlock(z *thebegateway.ZKProofRecord, block *config.ZKBlock) {
	if z == nil || block == nil {
		return
	}
	block.ProofHash = z.ProofHash
	block.StarkProof = z.StarkProof
	if len(z.Commitment) > 0 && len(z.Commitment)%4 == 0 {
		block.Commitment = make([]uint32, len(z.Commitment)/4)
		for i := range block.Commitment {
			block.Commitment[i] = binary.BigEndian.Uint32(z.Commitment[i*4:])
		}
	}
}

// nowNano returns the current time as Unix nanoseconds.
func nowNano() int64 { return time.Now().UTC().UnixNano() }
