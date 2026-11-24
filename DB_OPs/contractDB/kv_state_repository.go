package contractDB

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"

	"gossipnode/DB_OPs/cassata"
)

// kvKeyCode returns the BadgerDB derived key for a contract's bytecode.
func kvKeyCode(addr common.Address) []byte {
	return []byte("contract:code:" + addr.Hex())
}

// kvKeyStorage returns the BadgerDB derived key for a storage slot value.
func kvKeyStorage(addr common.Address, slot common.Hash) []byte {
	return []byte("contract:storage:" + addr.Hex() + ":" + slot.Hex())
}

// kvKeyStorageMeta returns the BadgerDB derived key for storage slot metadata.
func kvKeyStorageMeta(addr common.Address, slot common.Hash) []byte {
	return []byte("contract:storage_meta:" + addr.Hex() + ":" + slot.Hex())
}

// kvKeyNonce returns the BadgerDB derived key for a contract nonce.
func kvKeyNonce(addr common.Address) []byte {
	return []byte("contract:nonce:" + addr.Hex())
}

// kvKeyMeta returns the BadgerDB derived key for contract deployment metadata.
func kvKeyMeta(addr common.Address) []byte {
	return []byte("contract:meta:" + addr.Hex())
}

// kvTombstone is the value written for logical deletes (empty slot, deleted code, etc.).
var kvTombstone = []byte{}

// KVStateRepository implements StateRepository reading directly from BadgerDB
// via kv.Store.Get — bypassing SQL for hot-path EVM reads.
// Receipts are still read via cassata (SQL) as they are query-indexed there.
type KVStateRepository struct {
	kv  kv.Store
	cas *cassata.Cassata // for receipt reads only
}

var _ StateRepository = (*KVStateRepository)(nil)

// NewKVStateRepository creates a StateRepository backed by direct BadgerDB reads.
func NewKVStateRepository(kvStore kv.Store, cas *cassata.Cassata) *KVStateRepository {
	return &KVStateRepository{kv: kvStore, cas: cas}
}

// GetBalance is a stub — balances are owned by the DID service.
func (r *KVStateRepository) GetBalance(_ context.Context, _ common.Address) (*uint256.Int, error) {
	return nil, nil
}

func (r *KVStateRepository) GetCode(_ context.Context, addr common.Address) ([]byte, error) {
	v, err := r.kv.Get(kvKeyCode(addr))
	if err != nil {
		if isKVNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("KVStateRepository.GetCode: %w", err)
	}
	if len(v) == 0 {
		return nil, nil // tombstone → deleted
	}
	return v, nil
}

func (r *KVStateRepository) GetStorage(_ context.Context, addr common.Address, key common.Hash) (common.Hash, error) {
	v, err := r.kv.Get(kvKeyStorage(addr, key))
	if err != nil {
		if isKVNotFound(err) {
			return common.Hash{}, nil
		}
		return common.Hash{}, fmt.Errorf("KVStateRepository.GetStorage: %w", err)
	}
	if len(v) == 0 {
		return common.Hash{}, nil // tombstone
	}
	return common.HexToHash(string(v)), nil
}

func (r *KVStateRepository) GetStorageMetadata(_ context.Context, addr common.Address, key common.Hash) (*StorageMetadata, error) {
	v, err := r.kv.Get(kvKeyStorageMeta(addr, key))
	if err != nil {
		if isKVNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("KVStateRepository.GetStorageMetadata: %w", err)
	}
	if len(v) == 0 {
		return nil, nil // tombstone
	}
	var m StorageMetadata
	if err := json.Unmarshal(v, &m); err != nil {
		return nil, fmt.Errorf("KVStateRepository.GetStorageMetadata unmarshal: %w", err)
	}
	return &m, nil
}

func (r *KVStateRepository) GetNonce(_ context.Context, addr common.Address) (uint64, error) {
	v, err := r.kv.Get(kvKeyNonce(addr))
	if err != nil {
		if isKVNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("KVStateRepository.GetNonce: %w", err)
	}
	if len(v) == 0 {
		return 0, nil // tombstone
	}
	n, err := strconv.ParseUint(string(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("KVStateRepository.GetNonce parse: %w", err)
	}
	return n, nil
}

func (r *KVStateRepository) GetContractMetadata(_ context.Context, addr common.Address) ([]byte, error) {
	v, err := r.kv.Get(kvKeyMeta(addr))
	if err != nil {
		if isKVNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("KVStateRepository.GetContractMetadata: %w", err)
	}
	if len(v) == 0 || string(v) == "{}" {
		return nil, nil // tombstone
	}
	return v, nil
}

// GetReceipt reads from SQL via cassata — receipts are indexed there for block queries.
func (r *KVStateRepository) GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error) {
	if r.cas == nil {
		return nil, nil // repository constructed without cassata — receipts unavailable
	}
	res, err := r.cas.GetContractReceipt(ctx, txHash.Hex())
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("KVStateRepository.GetReceipt: %w", err)
	}
	return res.Raw, nil
}

func (r *KVStateRepository) NewBatch() StateBatch {
	return &KVStateBatch{repo: r, ops: nil}
}

// isKVNotFound returns true when the kv store signals key absence.
func isKVNotFound(err error) bool {
	if err == nil {
		return false
	}
	return err == kv.ErrKeyNotFound || strings.Contains(strings.ToLower(err.Error()), "key not found")
}

// isAccountNotFound reports whether err means the account simply does not exist —
// KV key-absence OR the SQL-backed "no rows in result set" that DB_OPs.GetAccount
// (via the AccountReader) returns for an unknown address. An unknown account is
// empty (balance 0, nonce 0), NOT a read failure, so getStateObject must not fail
// closed on it. Kept local so contractDB stays decoupled from gossipnode/DB_OPs.
func isAccountNotFound(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no rows in result set") || strings.Contains(m, "key not found")
}

// isNotFound reports whether err is a not-found condition from the SQL/cassata
// read path. (Recovered from the deleted thebe_adapter.go — its only surviving
// caller is GetReceipt above.)
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no rows") || strings.Contains(msg, "not found")
}
