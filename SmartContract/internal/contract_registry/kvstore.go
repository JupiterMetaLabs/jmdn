package contract_registry

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/SmartContract/internal/storage"
	"gossipnode/SmartContract/pkg/types"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// KVStoreRegistry implements RegistryDB using the generic KVStore interface.
type KVStoreRegistry struct {
	db storage.KVStore
	mu sync.RWMutex
}

// NewKVStoreRegistry creates a new registry instance.
func NewKVStoreRegistry(db storage.KVStore) *KVStoreRegistry {
	return &KVStoreRegistry{
		db: db,
	}
}

// Ensure interface compliance
var _ RegistryDB = (*KVStoreRegistry)(nil)

// RegisterContract persists a contract's metadata to the store.
func (r *KVStoreRegistry) RegisterContract(ctx context.Context, metadata *types.ContractMetadata) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := makeRegistryKey(metadata.Address)
	existing, err := r.db.Get(key)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("contract already exists at address %s", metadata.Address.Hex())
	}

	// Serialize
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal contract metadata: %w", err)
	}

	// Save
	return r.db.Set(key, data)
}

// GetContract retrieves a contract's metadata.
func (r *KVStoreRegistry) GetContract(ctx context.Context, address common.Address) (*types.ContractMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := makeRegistryKey(address)
	data, err := r.db.Get(key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("contract not found at address %s", address.Hex())
	}

	var metadata types.ContractMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contract data: %w", err)
	}

	return &metadata, nil
}

// ListContracts lists contracts using prefix iteration.
func (r *KVStoreRegistry) ListContracts(ctx context.Context, opts *ListOptions) ([]*types.ContractMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	iter, err := r.db.NewIterator(registryPrefix)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var contracts []*types.ContractMetadata
	var count uint64
	var offset uint64 = 0
	if opts != nil {
		offset = opts.Offset
	}

	for valid := iter.First(); valid; valid = iter.Next() {
		// Value is JSON
		val := iter.Value()
		var metadata types.ContractMetadata
		if err := json.Unmarshal(val, &metadata); err != nil {
			continue
		}

		// Apply Filters
		if opts != nil {
			if opts.Deployer != (common.Address{}) && metadata.Deployer != opts.Deployer {
				continue
			}
			if opts.FromTime > 0 && int64(metadata.DeployTime) < opts.FromTime {
				continue
			}
			if opts.ToTime > 0 && int64(metadata.DeployTime) > opts.ToTime {
				continue
			}
			if opts.FromBlock > 0 && metadata.DeployBlock < opts.FromBlock {
				continue
			}
			if opts.ToBlock > 0 && metadata.DeployBlock > opts.ToBlock {
				continue
			}
		}

		// Pagination: Offset
		if count < offset {
			count++
			continue
		}

		// Pagination: Limit
		if opts != nil && opts.Limit > 0 && uint32(len(contracts)) >= opts.Limit {
			break
		}

		contracts = append(contracts, &metadata)
		count++
	}

	return contracts, nil
}

// ContractExists checks if a contract exists at the given address
func (r *KVStoreRegistry) ContractExists(ctx context.Context, address common.Address) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := makeRegistryKey(address)
	val, err := r.db.Get(key)
	if err != nil {
		return false, err
	}
	return val != nil, nil
}

// GetTotalCount returns the total number of registered contracts by scanning keys
func (r *KVStoreRegistry) GetTotalCount(ctx context.Context) (uint64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	iter, err := r.db.NewIterator(registryPrefix)
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	var count uint64
	for valid := iter.First(); valid; valid = iter.Next() {
		count++
	}
	return count, nil
}

// Close closes the underlying db.
func (r *KVStoreRegistry) Close() error {
	return r.db.Close()
}

// Keys
var registryPrefix = []byte("registry:")

func makeRegistryKey(addr common.Address) []byte {
	return append(registryPrefix, addr.Bytes()...)
}
