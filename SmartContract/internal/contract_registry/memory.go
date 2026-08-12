package contract_registry

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/SmartContract/pkg/types"
)

// InMemoryRegistryDB implements RegistryDB in memory
type InMemoryRegistryDB struct {
	contracts map[common.Address]*types.ContractMetadata
	mutex     sync.RWMutex
}

// NewInMemoryRegistryDB creates a new in-memory contract registry
func NewInMemoryRegistryDB() *InMemoryRegistryDB {
	return &InMemoryRegistryDB{
		contracts: make(map[common.Address]*types.ContractMetadata),
	}
}

// RegisterContract stores a new deployed contract in the registry
// This implements the RegistryDB interface
func (db *InMemoryRegistryDB) RegisterContract(ctx context.Context, metadata *types.ContractMetadata) error {
	db.mutex.Lock()
	defer db.mutex.Unlock()

	if _, exists := db.contracts[metadata.Address]; exists {
		return fmt.Errorf("contract already exists at address %s", metadata.Address.Hex())
	}

	// Store a copy
	metadataCopy := *metadata
	db.contracts[metadata.Address] = &metadataCopy

	return nil
}

// GetContract retrieves contract metadata by address
func (db *InMemoryRegistryDB) GetContract(ctx context.Context, address common.Address) (*types.ContractMetadata, error) {
	db.mutex.RLock()
	defer db.mutex.RUnlock()

	metadata, exists := db.contracts[address]
	if !exists {
		return nil, fmt.Errorf("contract not found: %s", address.Hex())
	}

	// Return a copy
	metadataCopy := *metadata
	return &metadataCopy, nil
}

// ListContracts returns contracts matching the given options
func (db *InMemoryRegistryDB) ListContracts(ctx context.Context, opts *ListOptions) ([]*types.ContractMetadata, error) {
	db.mutex.RLock()
	defer db.mutex.RUnlock()

	if opts == nil {
		opts = &ListOptions{Limit: 100}
	}

	var result []*types.ContractMetadata

	// In-memory efficient filtering logic
	for _, meta := range db.contracts {
		// Filter by deployer
		if opts.Deployer != (common.Address{}) && meta.Deployer != opts.Deployer {
			continue
		}

		// Filter by time range
		if opts.FromTime > 0 && int64(meta.DeployTime) < opts.FromTime {
			continue
		}
		if opts.ToTime > 0 && int64(meta.DeployTime) > opts.ToTime {
			continue
		}

		// Filter by block range
		if opts.FromBlock > 0 && meta.DeployBlock < opts.FromBlock {
			continue
		}
		if opts.ToBlock > 0 && meta.DeployBlock > opts.ToBlock {
			continue
		}

		result = append(result, meta)
	}

	// Sorting by time desc (default)
	sort.Slice(result, func(i, j int) bool {
		return result[i].DeployTime > result[j].DeployTime
	})

	// Pagination
	if opts.Offset >= uint64(len(result)) {
		return []*types.ContractMetadata{}, nil
	}

	end := opts.Offset + uint64(opts.Limit)
	if end > uint64(len(result)) {
		end = uint64(len(result))
	}

	// Return copies
	finalResult := make([]*types.ContractMetadata, 0, end-opts.Offset)
	for i := opts.Offset; i < end; i++ {
		cpy := *result[i]
		finalResult = append(finalResult, &cpy)
	}

	return finalResult, nil
}

// ContractExists checks if a contract exists at the given address
func (db *InMemoryRegistryDB) ContractExists(ctx context.Context, address common.Address) (bool, error) {
	db.mutex.RLock()
	defer db.mutex.RUnlock()

	_, exists := db.contracts[address]
	return exists, nil
}

// GetTotalCount returns the total number of registered contracts
func (db *InMemoryRegistryDB) GetTotalCount(ctx context.Context) (uint64, error) {
	db.mutex.RLock()
	defer db.mutex.RUnlock()

	return uint64(len(db.contracts)), nil
}

// Close closes the database connection
func (db *InMemoryRegistryDB) Close() error {
	// Nothing to close for in-memory
	return nil
}
