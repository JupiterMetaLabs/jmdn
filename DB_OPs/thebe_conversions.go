package DB_OPs

// thebe_conversions.go — conversions between thebegateway record types and config domain types.
// Used by the ThebeHandle-based reimplementations of immuclient.go and account_immuclient.go.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
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

	// CommitteeCertificate: hydrate the persisted committee vote set (JSON) from
	// ExtraData so a synced/read block can re-verify its certificate (P-cert /
	// ThebeSync). Absent on the legacy prefix (blocks predating P-cert).
	committeeCert := ""
	if cc, ok := r.ExtraData["committee_certificate"]; ok {
		if s, ok2 := cc.(string); ok2 {
			committeeCert = s
		}
	}

	// StateFingerprint: hydrate the persisted P2.5 fingerprint so a synced/read
	// block carries it and ProcessBlockTransactions compares (halts on divergence)
	// rather than re-stamping. Absent on blocks stored before P2 persistence.
	stateFingerprint := ""
	if sf, ok := r.ExtraData["state_fingerprint"]; ok {
		if s, ok2 := sf.(string); ok2 {
			stateFingerprint = s
		}
	}

	// AccountNonces: hydrate the persisted canonical ART identities so a synced/read
	// block carries them and ProcessBlockTransactions creates new accounts with the
	// exact sequencer-assigned identity (no per-node recomputation). Absent on blocks
	// stored before this persistence landed.
	var accountNonces []config.AccountNonce
	if an, ok := r.ExtraData["account_nonces"]; ok {
		if s, ok2 := an.(string); ok2 && s != "" {
			_ = json.Unmarshal([]byte(s), &accountNonces)
		}
	}

	return &config.ZKBlock{
		BlockNumber:  r.BlockNumber,
		BlockHash:    common.HexToHash(r.BlockHash),
		PrevHash:     common.HexToHash(r.ParentHash),
		Timestamp:    r.Timestamp.Unix(),
		TxnsRoot:     r.TxsRoot,
		StateRoot:    common.HexToHash(r.StateRoot),
		LogsBloom:    r.LogsBloom,
		CoinbaseAddr: coinbase,
		ZKVMAddr:     zkvm,
		GasLimit:             r.GasLimit,
		GasUsed:              r.GasUsed,
		ExtraData:            extraData,
		CommitteeCertificate: committeeCert,
		StateFingerprint:     stateFingerprint,
		AccountNonces:        accountNonces,
		Transactions:         []config.Transaction{},
	}, nil
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
	// SigR/SigS are stored as base-16 (no 0x) via big.Int.Text(16) and CHAR(66)
	// pads with trailing spaces — trim before parsing. Without this, block full-tx
	// responses (eth_getBlockByNumber) return r=s=0 even though the row is signed.
	parseSig := func(s string) *big.Int {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "0x")
		s = strings.TrimPrefix(s, "0X")
		if s == "" {
			return nil
		}
		n, ok := new(big.Int).SetString(s, 16)
		if !ok {
			return nil
		}
		return n
	}
	if n := parseSig(r.SigR); n != nil {
		tx.R = n
	}
	if n := parseSig(r.SigS); n != nil {
		tx.S = n
	}

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
