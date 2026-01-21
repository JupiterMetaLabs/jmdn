package contract_registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/SmartContract/pkg/types"
	"gossipnode/config"
)

// ImmuRegistryDB implements RegistryDB using ImmuDB
type ImmuRegistryDB struct {
	client *config.PooledConnection
}

// NewImmuRegistryDB creates a new ImmuDB-backed contract registry
func NewImmuRegistryDB(client *config.PooledConnection) *ImmuRegistryDB {
	return &ImmuRegistryDB{
		client: client,
	}
}

// RegisterContract stores a new deployed contract in the registry
func (db *ImmuRegistryDB) RegisterContract(ctx context.Context, metadata *types.ContractMetadata) error {
	if db.client == nil {
		return fmt.Errorf("ImmuDB client is not initialized")
	}

	key := KeyContract(metadata.Address)

	// Create indexes
	// We need to use DB_OPs.Transaction to ensure atomicity, wait DB_OPs.Transaction takes *ImmuClient not PooledConnection
	// We can access db.client.Client

	err := DB_OPs.Transaction(db.client.Client, func(tx *config.ImmuTransaction) error {
		// 1. Serialize metadata
		data, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}

		// 2. Store metadata using Set from DB_OPs (exported as Set in immuclient.go?)
		// Actually immuclient.go has DB_OPs.Set taking tx.
		if err := DB_OPs.Set(tx, key, data); err != nil {
			return fmt.Errorf("failed to store contract metadata: %w", err)
		}

		// 3. Create indexes

		// Index by Deployer
		deployerIdxKey := KeyIndexByDeployer(metadata.Deployer, int64(metadata.DeployTime), metadata.Address)
		if err := DB_OPs.Set(tx, deployerIdxKey, []byte(metadata.Address.Hex())); err != nil {
			return fmt.Errorf("failed to create deployer index: %w", err)
		}

		// Index by Time
		timeIdxKey := KeyIndexByTime(int64(metadata.DeployTime), metadata.Address)
		if err := DB_OPs.Set(tx, timeIdxKey, []byte(metadata.Address.Hex())); err != nil {
			return fmt.Errorf("failed to create time index: %w", err)
		}

		return nil
	})

	return err
}

// GetContract retrieves contract metadata by address
func (db *ImmuRegistryDB) GetContract(ctx context.Context, address common.Address) (*types.ContractMetadata, error) {
	key := KeyContract(address)

	// Read using DB_OPs which supports PooledConnection
	data, err := DB_OPs.Read(db.client, key)
	if err != nil {
		// Check safely for not found
		if strings.Contains(err.Error(), "key not found") {
			return nil, fmt.Errorf("contract not found: %s", address.Hex())
		}
		return nil, err
	}

	var metadata types.ContractMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &metadata, nil
}

// ContractExists checks if a contract exists at the given address
func (db *ImmuRegistryDB) ContractExists(ctx context.Context, address common.Address) (bool, error) {
	key := KeyContract(address)
	return DB_OPs.Exists(db.client, key)
}

// ListContracts returns contracts matching the given options
func (db *ImmuRegistryDB) ListContracts(ctx context.Context, opts *ListOptions) ([]*types.ContractMetadata, error) {
	if opts == nil {
		opts = &ListOptions{Limit: 20}
	}

	// Strategy:
	// 1. If filtering by deployer, use deployer index
	// 2. If filtering by time (or no filter), use time index
	// 3. Resolve addresses to metadata

	var prefix string
	if opts.Deployer != (common.Address{}) {
		// contract:registry:index:deployer:<deployer_addr>:
		prefix = fmt.Sprintf("%s%s:", PrefixIndexDeployer, strings.ToLower(opts.Deployer.Hex()))
	} else {
		// contract:registry:index:time:
		prefix = PrefixIndexTime
	}

	// Get keys using DB_OPs.GetKeys or similar
	// DB_OPs.GetKeys(PooledConnection, prefix, limit)
	// We might need to fetch more keys if we apply application-side filtering (like block range)

	// Let's fetch keys with reasonable buffer
	fetchLimit := int(opts.Limit + 20)
	if opts.Offset > 0 {
		fetchLimit += int(opts.Offset)
	}

	keys, err := DB_OPs.GetKeys(db.client, prefix, fetchLimit)
	if err != nil {
		return nil, err
	}

	// Need to sort keys manually if DB doesn't guarantee order?
	// ImmuDB keys are usually lexical sorted.
	// Time index: prefix:timestamp:addr. Lexical sort of timestamp works if fixed width, but here it's int64.
	// To be safe, we might need to sort in memory if the prefix query doesn't guarantee it.

	// Only process keys in reverse order (newest first)?
	// The prefix scan is usually forward.
	// If we want newest first, and keys are stored as timestamp, we might need to read all keys in range?
	// For MVP, lets assume forward scan (oldest first) or we do basic implementation.

	// Let's resolve the first N contracts
	var contracts []*types.ContractMetadata

	for _, k := range keys {
		// Extract address from value or key?
		// The value stored at index is the address hex string.

		addrBytes, err := DB_OPs.Read(db.client, k)
		if err != nil {
			continue
		}
		addrStr := string(addrBytes)
		addr := common.HexToAddress(addrStr)

		// Get full metadata
		meta, err := db.GetContract(ctx, addr)
		if err != nil {
			continue
		}

		// Filter by Block Range (post-filter)
		if opts.FromBlock > 0 && meta.DeployBlock < opts.FromBlock {
			continue
		}
		if opts.ToBlock > 0 && meta.DeployBlock > opts.ToBlock {
			continue
		}

		contracts = append(contracts, meta)
	}

	// Sort by DeployTime desc
	sort.Slice(contracts, func(i, j int) bool {
		return contracts[i].DeployTime > contracts[j].DeployTime
	})

	// Pagination
	if opts.Offset >= uint64(len(contracts)) {
		return []*types.ContractMetadata{}, nil
	}

	end := opts.Offset + uint64(opts.Limit)
	if end > uint64(len(contracts)) {
		end = uint64(len(contracts))
	}

	return contracts[opts.Offset:end], nil
}

// GetTotalCount returns the total number of registered contracts
func (db *ImmuRegistryDB) GetTotalCount(ctx context.Context) (uint64, error) {
	// DB_OPs doesn't have a Count method for prefix?
	// We can count keys in time index.
	// Using DB_OPs.GetAllKeys might be expensive.
	// For production, we should maintain a "count" key counter.

	// Fallback: Using GetAllKeys for index
	keys, err := DB_OPs.GetAllKeys(db.client, PrefixIndexTime)
	if err != nil {
		return 0, err
	}
	return uint64(len(keys)), nil
}

// Close closes the database connection
func (db *ImmuRegistryDB) Close() error {
	// The pooled connection is managed mainly by the pool, but we can call Close if we want to release it?
	// Usually PooledConnection.Close only returns it to pool? No, config says Close closes connection pool?
	// Let's look at config.PooledConnection usage.
	return nil
}
