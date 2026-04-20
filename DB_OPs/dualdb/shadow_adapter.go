package dualdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ShadowAdapter translates immudb write payloads into cassata typed ingest calls.
type ShadowAdapter struct {
	cas *cassata.Cassata
}

func NewShadowAdapter(cas *cassata.Cassata) *ShadowAdapter {
	return &ShadowAdapter{cas: cas}
}

func (s *ShadowAdapter) Create(_ *config.PooledConnection, key string, value interface{}) error {
	return s.ingestKV(context.Background(), key, value)
}

func (s *ShadowAdapter) SafeCreate(_ *config.ImmuClient, key string, value interface{}) error {
	return s.ingestKV(context.Background(), key, value)
}

func (s *ShadowAdapter) BatchCreate(_ *config.PooledConnection, entries map[string]interface{}) error {
	ctx := context.Background()
	for k, v := range entries {
		if err := s.ingestKV(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *ShadowAdapter) CreateAccount(_ *config.PooledConnection, didAddress string, address common.Address, metadata map[string]interface{}) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("shadow adapter: marshal account metadata: %w", err)
	}

	did := didAddress
	account := cassata.AccountResult{
		Address:     address.Hex(),
		DIDAddress:  &did,
		BalanceWei:  "0",
		Nonce:       "0",
		AccountType: 0,
		Metadata:    meta,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	return s.cas.IngestAccount(context.Background(), account)
}

func (s *ShadowAdapter) UpdateAccountBalance(_ *config.PooledConnection, address common.Address, newBalance string) error {
	account := cassata.AccountResult{
		Address:     address.Hex(),
		BalanceWei:  newBalance,
		Nonce:       "0",
		AccountType: 0,
		Metadata:    json.RawMessage("{}"),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	return s.cas.IngestAccount(context.Background(), account)
}

func (s *ShadowAdapter) StoreZKBlock(_ *config.PooledConnection, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("shadow adapter: nil block")
	}

	ctx := context.Background()
	if err := s.cas.IngestBlock(ctx, blockToCassata(block)); err != nil {
		return err
	}

	stark := block.StarkProof
	if stark == nil {
		stark = []byte{}
	}
	if err := s.cas.IngestZKProof(ctx, cassata.ZKProofResult{
		ProofHash:   block.ProofHash,
		BlockNumber: block.BlockNumber,
		StarkProof:  stark,
		Commitment:  uint32SliceToBytes(block.Commitment),
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}

	// prev_snapshot_id must reference snapshots.snapshot_id (IDENTITY), not block
	// numbers. We do not have the prior row id here without a DB read, so leave
	// chain unset (NULL). Snapshot rows remain keyed by block_number / block_hash.
	if err := s.cas.IngestSnapshot(ctx, cassata.SnapshotResult{
		SnapshotID:     int64(block.BlockNumber),
		BlockNumber:    block.BlockNumber,
		BlockHash:      block.BlockHash.Hex(),
		PrevSnapshotID: nil,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		return err
	}

	for i := range block.Transactions {
		if err := s.cas.IngestTx(ctx, txToCassata(block, i)); err != nil {
			return err
		}
	}

	return nil
}

func (s *ShadowAdapter) ingestKV(ctx context.Context, key string, value interface{}) error {
	if key == "" || s.cas == nil {
		return nil
	}

	switch {
	case strings.HasPrefix(key, DB_OPs.Prefix):
		acc, ok := decodeAccount(value)
		if !ok {
			return nil
		}
		return s.cas.IngestAccount(ctx, accountToCassata(acc))

	case strings.HasPrefix(key, DB_OPs.PREFIX_BLOCK):
		block, ok := decodeBlock(value)
		if !ok {
			return nil
		}
		return s.StoreZKBlock(nil, block)

	case strings.HasPrefix(key, DB_OPs.DEFAULT_PREFIX_TX):
		tx, ok := decodeTx(value)
		if !ok {
			return nil
		}
		return s.cas.IngestTx(ctx, tx)

	case strings.HasPrefix(key, "zk:"):
		zk, ok := decodeZKProof(value)
		if !ok {
			return nil
		}
		return s.cas.IngestZKProof(ctx, zk)

	case strings.HasPrefix(key, "snapshot:"):
		snap, ok := decodeSnapshot(value)
		if !ok {
			return nil
		}
		return s.cas.IngestSnapshot(ctx, snap)
	}

	return nil
}

func decodeAccount(v interface{}) (*DB_OPs.Account, bool) {
	switch t := v.(type) {
	case *DB_OPs.Account:
		return t, true
	case DB_OPs.Account:
		cp := t
		return &cp, true
	case []byte:
		var a DB_OPs.Account
		if err := json.Unmarshal(t, &a); err != nil {
			return nil, false
		}
		return &a, true
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var a DB_OPs.Account
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, false
		}
		return &a, true
	}
}

func decodeBlock(v interface{}) (*config.ZKBlock, bool) {
	switch t := v.(type) {
	case *config.ZKBlock:
		return t, true
	case config.ZKBlock:
		cp := t
		return &cp, true
	case []byte:
		var b config.ZKBlock
		if err := json.Unmarshal(t, &b); err != nil {
			return nil, false
		}
		return &b, true
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var b config.ZKBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, false
		}
		return &b, true
	}
}

func decodeTx(v interface{}) (cassata.TxResult, bool) {
	switch t := v.(type) {
	case cassata.TxResult:
		return t, true
	case *cassata.TxResult:
		return *t, true
	default:
		return cassata.TxResult{}, false
	}
}

func decodeZKProof(v interface{}) (cassata.ZKProofResult, bool) {
	switch t := v.(type) {
	case cassata.ZKProofResult:
		return t, true
	case *cassata.ZKProofResult:
		return *t, true
	default:
		return cassata.ZKProofResult{}, false
	}
}

func decodeSnapshot(v interface{}) (cassata.SnapshotResult, bool) {
	switch t := v.(type) {
	case cassata.SnapshotResult:
		return t, true
	case *cassata.SnapshotResult:
		return *t, true
	default:
		return cassata.SnapshotResult{}, false
	}
}

func accountToCassata(a *DB_OPs.Account) cassata.AccountResult {
	var did *string
	if a.DIDAddress != "" {
		did = &a.DIDAddress
	}

	meta, _ := json.Marshal(a.Metadata)
	return cassata.AccountResult{
		Address:     a.Address.Hex(),
		DIDAddress:  did,
		BalanceWei:  a.Balance,
		Nonce:       strconv.FormatUint(a.Nonce, 10),
		AccountType: 0,
		Metadata:    meta,
		CreatedAt:   time.Unix(0, a.CreatedAt).UTC(),
		UpdatedAt:   time.Unix(0, a.UpdatedAt).UTC(),
	}
}

func blockToCassata(b *config.ZKBlock) cassata.BlockResult {
	status := int16(0)
	if strings.EqualFold(b.Status, "confirmed") || strings.EqualFold(b.Status, "finalized") {
		status = 1
	}

	var coinbase *string
	if b.CoinbaseAddr != nil {
		s := b.CoinbaseAddr.Hex()
		coinbase = &s
	}
	var zkvm *string
	if b.ZKVMAddr != nil {
		s := b.ZKVMAddr.Hex()
		zkvm = &s
	}

	gasLimit := strconv.FormatUint(b.GasLimit, 10)
	gasUsed := strconv.FormatUint(b.GasUsed, 10)
	extra, _ := json.Marshal(map[string]string{"extra_data": b.ExtraData})
	stateRoot := b.StateRoot.Hex()
	txRoot := b.TxnsRoot

	return cassata.BlockResult{
		BlockNumber:  b.BlockNumber,
		BlockHash:    b.BlockHash.Hex(),
		ParentHash:   b.PrevHash.Hex(),
		Timestamp:    time.Unix(b.Timestamp, 0).UTC(),
		TxsRoot:      &txRoot,
		StateRoot:    &stateRoot,
		LogsBloom:    b.LogsBloom,
		CoinbaseAddr: coinbase,
		ZKVMAddr:     zkvm,
		GasLimit:     &gasLimit,
		GasUsed:      &gasUsed,
		Status:       status,
		ExtraData:    extra,
		CreatedAt:    time.Now().UTC(),
	}
}

func txToCassata(block *config.ZKBlock, idx int) cassata.TxResult {
	tx := block.Transactions[idx]
	from := ""
	if tx.From != nil {
		from = tx.From.Hex()
	}
	var to *string
	if tx.To != nil {
		s := tx.To.Hex()
		to = &s
	}

	gasLimit := strconv.FormatUint(tx.GasLimit, 10)
	value := "0"
	if tx.Value != nil {
		value = tx.Value.String()
	}
	nonce := strconv.FormatUint(tx.Nonce, 10)

	var gasPrice *string
	if tx.GasPrice != nil {
		s := tx.GasPrice.String()
		gasPrice = &s
	}
	var maxFee *string
	if tx.MaxFee != nil {
		s := tx.MaxFee.String()
		maxFee = &s
	}
	var maxPriority *string
	if tx.MaxPriorityFee != nil {
		s := tx.MaxPriorityFee.String()
		maxPriority = &s
	}
	var sigV *int
	if tx.V != nil {
		v := int(tx.V.Int64())
		sigV = &v
	}
	var sigR *string
	if tx.R != nil {
		s := tx.R.Text(16)
		if !strings.HasPrefix(s, "0x") {
			s = "0x" + s
		}
		sigR = &s
	}
	var sigS *string
	if tx.S != nil {
		s := tx.S.Text(16)
		if !strings.HasPrefix(s, "0x") {
			s = "0x" + s
		}
		sigS = &s
	}

	accessList, _ := json.Marshal(tx.AccessList)
	out := cassata.TxResult{
		TxHash:            tx.Hash.Hex(),
		BlockNumber:       block.BlockNumber,
		TxIndex:           idx,
		FromAddr:          from,
		ToAddr:            to,
		ValueWei:          value,
		Nonce:             nonce,
		Type:              int16(tx.Type),
		GasLimit:          &gasLimit,
		GasPriceWei:       gasPrice,
		MaxFeeWei:         maxFee,
		MaxPriorityFeeWei: maxPriority,
		Data:              tx.Data,
		AccessList:        accessList,
		SigV:              sigV,
		SigR:              sigR,
		SigS:              sigS,
		CreatedAt:         time.Now().UTC(),
	}
	normalizeTxFeesForThebeSchema(&out)
	return out
}

// normalizeTxFeesForThebeSchema enforces thebeprofile chk_txn_fee_model, which
// requires gas_price_wei for type 0/1 and max_fee + max_priority for type 2.
// ZK/immudb payloads may omit fee fields that were not persisted on the block tx.
func normalizeTxFeesForThebeSchema(t *cassata.TxResult) {
	z := "0"
	switch t.Type {
	case 0, 1:
		if t.GasPriceWei == nil {
			t.GasPriceWei = &z
		}
	case 2:
		if t.MaxFeeWei == nil {
			t.MaxFeeWei = &z
		}
		if t.MaxPriorityFeeWei == nil {
			t.MaxPriorityFeeWei = &z
		}
	default:
		if t.GasPriceWei == nil {
			t.GasPriceWei = &z
		}
	}
}

func uint32SliceToBytes(in []uint32) []byte {
	if len(in) == 0 {
		return nil
	}
	out := make([]byte, 0, len(in)*4)
	for _, v := range in {
		out = append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	return out
}
