package contract_registry

import (
	"context"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/SmartContract/pkg/types"
)

// RegistryDB defines the interface for contract registry operations
// This stores and queries contract metadata in contractsdb
type RegistryDB interface {
	// RegisterContract stores a new deployed contract in the registry
	RegisterContract(ctx context.Context, metadata *types.ContractMetadata) error

	// GetContract retrieves contract metadata by address
	GetContract(ctx context.Context, address common.Address) (*types.ContractMetadata, error)

	// ListContracts returns contracts matching the given options
	// If limit is 0, a default limit is applied
	ListContracts(ctx context.Context, opts *ListOptions) ([]*types.ContractMetadata, error)

	// ContractExists checks if a contract exists at the given address
	ContractExists(ctx context.Context, address common.Address) (bool, error)

	// GetTotalCount returns the total number of registered contracts
	GetTotalCount(ctx context.Context) (uint64, error)

	// Close closes the database connection
	Close() error
}

// ListOptions defines filtering options for listing contracts
type ListOptions struct {
	// Filter by deployer address (optional)
	Deployer common.Address

	// Filter by time range (optional)
	FromTime int64
	ToTime   int64

	// Filter by block range (optional)
	FromBlock uint64
	ToBlock   uint64

	// Pagination
	Offset uint64
	Limit  uint32
}
