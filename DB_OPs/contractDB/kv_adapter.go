package contractDB

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// KVStateRepository implements StateRepository on top of KVStore.
type KVStateRepository struct {
	db KVStore
}

// Ensure KVStateRepository satisfies StateRepository at compile time.
var _ StateRepository = (*KVStateRepository)(nil)

// NewKVStateRepository creates a StateRepository backed by the given KVStore.
func NewKVStateRepository(db KVStore) *KVStateRepository {
	return &KVStateRepository{db: db}
}

func (r *KVStateRepository) GetCode(ctx context.Context, addr common.Address) ([]byte, error) {
	return r.db.Get(makeCodeKey(addr))
}

func (r *KVStateRepository) GetStorage(ctx context.Context, addr common.Address, hash common.Hash) (common.Hash, error) {
	val, err := r.db.Get(makeStorageKey(addr, hash))
	if err != nil || len(val) == 0 {
		return common.Hash{}, nil
	}
	return common.BytesToHash(val), nil
}

func (r *KVStateRepository) GetStorageMetadata(ctx context.Context, addr common.Address, hash common.Hash) (*StorageMetadata, error) {
	val, err := r.db.Get(makeStorageMetaKey(addr, hash))
	if err != nil || len(val) == 0 {
		return nil, nil
	}
	var meta StorageMetadata
	if err := json.Unmarshal(val, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *KVStateRepository) GetNonce(ctx context.Context, addr common.Address) (uint64, error) {
	val, err := r.db.Get(makeNonceKey(addr))
	if err != nil || len(val) == 0 {
		return 0, nil
	}
	return new(big.Int).SetBytes(val).Uint64(), nil
}

// GetBalance is a stub — balances are managed by the DID service.
func (r *KVStateRepository) GetBalance(ctx context.Context, addr common.Address) (*uint256.Int, error) {
	return nil, nil
}

func (r *KVStateRepository) GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error) {
	return r.db.Get(makeContractMetaKey(addr))
}

func (r *KVStateRepository) GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error) {
	return r.db.Get(makeReceiptKey(txHash))
}

func (r *KVStateRepository) NewBatch() StateBatch {
	return &KVBatch{batch: r.db.NewBatch()}
}

// KVBatch implements StateBatch for KVStateRepository.
type KVBatch struct {
	batch Batch
}

var _ StateBatch = (*KVBatch)(nil)

func (b *KVBatch) SaveCode(addr common.Address, code []byte) error {
	return b.batch.Set(makeCodeKey(addr), code)
}

func (b *KVBatch) SaveStorage(addr common.Address, hash common.Hash, value common.Hash) error {
	return b.batch.Set(makeStorageKey(addr, hash), value.Bytes())
}

func (b *KVBatch) SaveStorageMetadata(addr common.Address, hash common.Hash, meta StorageMetadata) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return b.batch.Set(makeStorageMetaKey(addr, hash), raw)
}

func (b *KVBatch) SaveNonce(addr common.Address, nonce uint64) error {
	nonceBytes := new(big.Int).SetUint64(nonce).Bytes()
	return b.batch.Set(makeNonceKey(addr), nonceBytes)
}

func (b *KVBatch) SaveContractMetadata(addr common.Address, data []byte) error {
	return b.batch.Set(makeContractMetaKey(addr), data)
}

func (b *KVBatch) SaveReceipt(txHash common.Hash, data []byte) error {
	return b.batch.Set(makeReceiptKey(txHash), data)
}

func (b *KVBatch) DeleteCode(addr common.Address) error {
	return b.batch.Delete(makeCodeKey(addr))
}

func (b *KVBatch) DeleteStorage(addr common.Address, hash common.Hash) error {
	return b.batch.Delete(makeStorageKey(addr, hash))
}

func (b *KVBatch) DeleteStorageMetadata(addr common.Address, hash common.Hash) error {
	return b.batch.Delete(makeStorageMetaKey(addr, hash))
}

func (b *KVBatch) DeleteNonce(addr common.Address) error {
	return b.batch.Delete(makeNonceKey(addr))
}

func (b *KVBatch) DeleteReceipt(txHash common.Hash) error {
	return b.batch.Delete(makeReceiptKey(txHash))
}

func (b *KVBatch) Commit() error { return b.batch.Commit() }
func (b *KVBatch) Close() error  { return b.batch.Close() }

func makeCodeKey(addr common.Address) []byte {
	key := make([]byte, len(PrefixCode)+common.AddressLength)
	copy(key, PrefixCode)
	copy(key[len(PrefixCode):], addr[:])
	return key
}

func makeStorageKey(addr common.Address, hash common.Hash) []byte {
	key := make([]byte, len(PrefixStorage)+common.AddressLength+common.HashLength)
	copy(key, PrefixStorage)
	copy(key[len(PrefixStorage):], addr[:])
	copy(key[len(PrefixStorage)+common.AddressLength:], hash[:])
	return key
}

func makeStorageMetaKey(addr common.Address, hash common.Hash) []byte {
	key := make([]byte, len(PrefixStorageMeta)+common.AddressLength+common.HashLength)
	copy(key, PrefixStorageMeta)
	copy(key[len(PrefixStorageMeta):], addr[:])
	copy(key[len(PrefixStorageMeta)+common.AddressLength:], hash[:])
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
