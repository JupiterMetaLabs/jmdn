package contract_registry

import (
	"fmt"

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/database"
)

// RegistryFactory creates contract registry database instances
type RegistryFactory struct {
	config *database.Config
}

// NewRegistryFactory creates a new registry factory with the given configuration
func NewRegistryFactory(config *database.Config) (*RegistryFactory, error) {
	if config == nil {
		return nil, fmt.Errorf("database config cannot be nil")
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid database config: %w", err)
	}

	return &RegistryFactory{
		config: config,
	}, nil
}

// CreateRegistryDB creates a RegistryDB implementation based on configuration
func (f *RegistryFactory) CreateRegistryDB(sharedStore contractDB.KVStore) (RegistryDB, error) {
	switch f.config.Type {
	case database.DBTypeInMemory:
		return NewInMemoryRegistryDB(), nil
	default:
		return nil, fmt.Errorf("unsupported database type for RegistryDB: %s (only memory is supported)", f.config.Type)
	}
}

// deprecated
