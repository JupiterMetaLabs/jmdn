package contractDB

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// PebbleAdapter implements StateRepository using the KVStore interface (backed by PebbleDB).
// It is the canonical production implementation of StateRepository.
type PebbleAdapter struct {
	db KVStore
}

// Ensure PebbleAdapter satisfies StateRepository at compile time.
var _ StateRepository = (*PebbleAdapter)(nil)

// NewPebbleAdapter creates a StateRepository backed by the given KVStore.
func NewPebbleAdapter(db KVStore) *PebbleAdapter {
	return &PebbleAdapter{db: db}
}

// ============================================================================
// Read operations
// ============================================================================

func (p *PebbleAdapter) GetCode(ctx context.Context, addr common.Address) ([]byte, error) {
	return p.db.Get(makeCodeKey(addr))
}

func (p *PebbleAdapter) GetStorage(ctx context.Context, addr common.Address, hash common.Hash) (common.Hash, error) {
	val, err := p.db.Get(makeStorageKey(addr, hash))
	if err != nil || len(val) == 0 {
		return common.Hash{}, nil
	}
	return common.BytesToHash(val), nil
}

func (p *PebbleAdapter) GetStorageMetadata(ctx context.Context, addr common.Address, hash common.Hash) (*StorageMetadata, error) {
	val, err := p.db.Get(makeStorageMetaKey(addr, hash))
	if err != nil || len(val) == 0 {
		return nil, nil
	}
	var meta StorageMetadata
	if err := json.Unmarshal(val, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (p *PebbleAdapter) GetNonce(ctx context.Context, addr common.Address) (uint64, error) {
	val, err := p.db.Get(makeNonceKey(addr))
	if err != nil || len(val) == 0 {
		return 0, nil
	}
	return new(big.Int).SetBytes(val).Uint64(), nil
}

// GetBalance is a stub — balances are managed by the DID service, not PebbleDB.
func (p *PebbleAdapter) GetBalance(ctx context.Context, addr common.Address) (*uint256.Int, error) {
	return nil, nil
}

func (p *PebbleAdapter) GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error) {
	return p.db.Get(makeContractMetaKey(addr))
}

func (p *PebbleAdapter) GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error) {
	return p.db.Get(makeReceiptKey(txHash))
}

// ============================================================================
// Batch writes
// ============================================================================

func (p *PebbleAdapter) NewBatch() StateBatch {
	return &PebbleBatch{batch: p.db.NewBatch()}
}

// PebbleBatch implements StateBatch for the PebbleAdapter.
type PebbleBatch struct {
	batch Batch
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
	return b.batch.Set(makeNonceKey(addr), new(big.Int).SetUint64(nonce).Bytes())
}

func (b *PebbleBatch) DeleteNonce(addr common.Address) error {
	return b.batch.Delete(makeNonceKey(addr))
}

func (b *PebbleBatch) SaveContractMetadata(addr common.Address, data []byte) error {
	return b.batch.Set(makeContractMetaKey(addr), data)
}

func (b *PebbleBatch) SaveReceipt(txHash common.Hash, data []byte) error {
	return b.batch.Set(makeReceiptKey(txHash), data)
}

func (b *PebbleBatch) Commit() error { return b.batch.Commit() }
func (b *PebbleBatch) Close() error  { return b.batch.Close() }

// ============================================================================
// Key helpers
// Each function allocates a fresh slice to avoid mutating the shared prefix
// constant's backing array (Go's append can overwrite capacity beyond len).
// ============================================================================

func makeCodeKey(addr common.Address) []byte {
	key := make([]byte, len(PrefixCode)+common.AddressLength)
	copy(key, PrefixCode)
	copy(key[len(PrefixCode):], addr[:])
	return key
}

func makeStorageKey(addr common.Address, slot common.Hash) []byte {
	key := make([]byte, len(PrefixStorage)+common.AddressLength+common.HashLength)
	copy(key, PrefixStorage)
	copy(key[len(PrefixStorage):], addr[:])
	copy(key[len(PrefixStorage)+common.AddressLength:], slot[:])
	return key
}

func makeStorageMetaKey(addr common.Address, slot common.Hash) []byte {
	key := make([]byte, len(PrefixStorageMeta)+common.AddressLength+common.HashLength)
	copy(key, PrefixStorageMeta)
	copy(key[len(PrefixStorageMeta):], addr[:])
	copy(key[len(PrefixStorageMeta)+common.AddressLength:], slot[:])
	return key
}

func makeNonceKey(addr common.Address) []byte {
	key := make([]byte, len(PrefixNonce)+common.AddressLength)
	copy(key, PrefixNonce)
	copy(key[len(PrefixNonce):], addr[:])
	return key
}

func makeContractMetaKey(addr common.Address) []byte {
	key := make([]byte, len(PrefixContractMeta)+common.AddressLength)
	copy(key, PrefixContractMeta)
	copy(key[len(PrefixContractMeta):], addr[:])
	return key
}

func makeReceiptKey(txHash common.Hash) []byte {
	key := make([]byte, len(PrefixReceipt)+common.HashLength)
	copy(key, PrefixReceipt)
	copy(key[len(PrefixReceipt):], txHash[:])
	return key
}
