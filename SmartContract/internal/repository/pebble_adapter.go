package repository

import (
	"context"
	"encoding/json"
	"math/big"

	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

var (
	PrefixCode         = []byte("code:")
	PrefixStorage      = []byte("storage:")
	PrefixNonce        = []byte("nonce:")
	PrefixStorageMeta  = []byte("meta:storage:")
	PrefixContractMeta = []byte("meta:contract:")
	PrefixReceipt      = []byte("receipt:")
)

// PebbleAdapter implements the StateRepository interface using the existing storage.KVStore (PebbleDB).
// This serves as the Phase 1 migration step to decouple ContractDB from Pebble specifics.
type PebbleAdapter struct {
	db storage.KVStore
}

func NewPebbleAdapter(db storage.KVStore) *PebbleAdapter {
	return &PebbleAdapter{db: db}
}

// ==========================================
// DB Read Operations
// ==========================================

func (p *PebbleAdapter) GetCode(ctx context.Context, addr common.Address) ([]byte, error) {
	key := makeCodeKey(addr)
	val, err := p.db.Get(key)
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (p *PebbleAdapter) GetStorage(ctx context.Context, addr common.Address, hash common.Hash) (common.Hash, error) {
	key := makeStorageKey(addr, hash)
	val, err := p.db.Get(key)
	if err != nil || len(val) == 0 {
		return common.Hash{}, nil
	}
	return common.BytesToHash(val), nil
}

func (p *PebbleAdapter) GetStorageMetadata(ctx context.Context, addr common.Address, hash common.Hash) (*StorageMetadata, error) {
	key := makeStorageMetaKey(addr, hash)
	val, err := p.db.Get(key)
	if err != nil || len(val) == 0 {
		return nil, nil // Not found
	}

	var meta StorageMetadata
	if err := json.Unmarshal(val, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (p *PebbleAdapter) GetNonce(ctx context.Context, addr common.Address) (uint64, error) {
	key := makeNonceKey(addr)
	val, err := p.db.Get(key)
	if err != nil || len(val) == 0 {
		return 0, nil
	}
	return new(big.Int).SetBytes(val).Uint64(), nil
}

func (p *PebbleAdapter) GetBalance(ctx context.Context, addr common.Address) (*uint256.Int, error) {
	// PebbleDB in current JMDN architecture does NOT store balances (DB_OPs does).
	// This method is a stub for the interface.
	return nil, nil
}

// ==========================================
// Metadata & Receipts
// ==========================================

func (p *PebbleAdapter) GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error) {
	key := append(PrefixContractMeta, addr.Bytes()...)
	return p.db.Get(key)
}

func (p *PebbleAdapter) GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error) {
	key := append(PrefixReceipt, txHash.Bytes()...)
	return p.db.Get(key)
}

// ==========================================
// Transactional Writes
// ==========================================

func (p *PebbleAdapter) NewBatch() StateBatch {
	return &PebbleBatch{
		batch: p.db.NewBatch(),
	}
}

// PebbleBatch implements the StateBatch interface for writing to PebbleDB.
type PebbleBatch struct {
	batch storage.Batch
}

func (b *PebbleBatch) SaveCode(addr common.Address, code []byte) error {
	return b.batch.Set(makeCodeKey(addr), code)
}

func (b *PebbleBatch) DeleteCode(addr common.Address) error {
	return b.batch.Delete(makeCodeKey(addr))
}

func (b *PebbleBatch) SaveStorage(addr common.Address, key common.Hash, value common.Hash) error {
	return b.batch.Set(makeStorageKey(addr, key), value[:])
}

func (b *PebbleBatch) DeleteStorage(addr common.Address, key common.Hash) error {
	return b.batch.Delete(makeStorageKey(addr, key))
}

func (b *PebbleBatch) SaveStorageMetadata(addr common.Address, key common.Hash, meta StorageMetadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return b.batch.Set(makeStorageMetaKey(addr, key), data)
}

func (b *PebbleBatch) DeleteStorageMetadata(addr common.Address, key common.Hash) error {
	return b.batch.Delete(makeStorageMetaKey(addr, key))
}

func (b *PebbleBatch) SaveNonce(addr common.Address, nonce uint64) error {
	nonceBytes := new(big.Int).SetUint64(nonce).Bytes()
	return b.batch.Set(makeNonceKey(addr), nonceBytes)
}

func (b *PebbleBatch) DeleteNonce(addr common.Address) error {
	return b.batch.Delete(makeNonceKey(addr))
}

func (b *PebbleBatch) SaveContractMetadata(addr common.Address, data []byte) error {
	key := append(PrefixContractMeta, addr.Bytes()...)
	return b.batch.Set(key, data)
}

func (b *PebbleBatch) SaveReceipt(txHash common.Hash, data []byte) error {
	key := append(PrefixReceipt, txHash.Bytes()...)
	return b.batch.Set(key, data)
}

func (b *PebbleBatch) Commit() error {
	return b.batch.Commit()
}

func (b *PebbleBatch) Close() error {
	return b.batch.Close()
}

// ==========================================
// Helper Key Formats
// ==========================================

func makeCodeKey(addr common.Address) []byte {
	return append(PrefixCode, addr.Bytes()...)
}

func makeStorageKey(addr common.Address, key common.Hash) []byte {
	return append(PrefixStorage, append(addr.Bytes(), key.Bytes()...)...)
}

func makeStorageMetaKey(addr common.Address, key common.Hash) []byte {
	return append(PrefixStorageMeta, append(addr.Bytes(), key.Bytes()...)...)
}

func makeNonceKey(addr common.Address) []byte {
	return append(PrefixNonce, addr.Bytes()...)
}
