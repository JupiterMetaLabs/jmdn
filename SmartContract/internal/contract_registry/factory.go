package contract_registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/database"
)

// Global singleton registry factory instance
var (
	defaultRegistryFactory  *RegistryFactory
	registryFactoryInitOnce sync.Once
	registryFactoryMutex    sync.RWMutex
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

// DefaultRegistryFactory returns the global singleton registry factory instance
func DefaultRegistryFactory() (*RegistryFactory, error) {
	var err error
	registryFactoryInitOnce.Do(func() {
		var config *database.Config
		config = database.LoadConfigFromEnv()
		defaultRegistryFactory, err = NewRegistryFactory(config)
	})
	return defaultRegistryFactory, err
}

// CreateRegistryDB creates a RegistryDB implementation based on configuration
func (f *RegistryFactory) CreateRegistryDB(sharedStore contractDB.KVStore) (RegistryDB, error) {
	// If shared store is provided, prefer that for Pebble
	if sharedStore != nil && f.config.Type == database.DBTypePebble {
		return NewKVStoreRegistry(sharedStore), nil
	}

	switch f.config.Type {
	case database.DBTypeInMemory:
		return NewInMemoryRegistryDB(), nil
	case database.DBTypePebble:
		// Fallback create new store if not provided (though main.go should provide it)
		// We use a separate path suffix if we are force-creating it here to avoid lock
		// But ideally this path shouldn't be hit if main.go does its job
		path := "contract_registry_pebble"
		if f.config.Path != "" {
			path = strings.TrimSuffix(f.config.Path, "/") + "_registry"
		}

		store, err := contractDB.NewKVStore(contractDB.Config{
			Type: contractDB.StoreTypePebble,
			Path: path,
		})
		if err != nil {
			return nil, err
		}
		return NewKVStoreRegistry(store), nil
	default:
		return nil, fmt.Errorf("unsupported database type for RegistryDB: %s", f.config.Type)
	}
}

// createImmuRegistryDB creates an ImmuDB-backed RegistryDB
func (f *RegistryFactory) createImmuRegistryDB() (RegistryDB, error) {
	// deprecated
	return nil, fmt.Errorf("immudb deprecated")
}

// createInMemoryRegistryDB creates an in-memory RegistryDB
func (f *RegistryFactory) createInMemoryRegistryDB() (RegistryDB, error) {
	return NewInMemoryRegistryDB(), nil
}

// WithContext returns a context-aware wrapper for database operations
func (f *RegistryFactory) WithContext(ctx context.Context) *ContextualRegistryFactory {
	return &ContextualRegistryFactory{
		factory: f,
		ctx:     ctx,
	}
}

// ContextualRegistryFactory wraps RegistryFactory with a context
type ContextualRegistryFactory struct {
	factory *RegistryFactory
	ctx     context.Context
}

// CreateRegistryDB creates a RegistryDB with the bound context
func (cf *ContextualRegistryFactory) CreateRegistryDB(sharedStore contractDB.KVStore) (RegistryDB, error) {
	return cf.factory.CreateRegistryDB(sharedStore)
}
