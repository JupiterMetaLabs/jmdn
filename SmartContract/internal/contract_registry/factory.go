package contract_registry

import (
	"context"
	"fmt"
	"sync"

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
func (f *RegistryFactory) CreateRegistryDB() (RegistryDB, error) {
	switch f.config.Type {
	case database.DBTypeImmuDB:
		return f.createImmuRegistryDB()
	case database.DBTypeInMemory:
		return f.createInMemoryRegistryDB()
	default:
		return nil, fmt.Errorf("unsupported database type for RegistryDB: %s", f.config.Type)
	}
}

// createImmuRegistryDB creates an ImmuDB-backed RegistryDB
func (f *RegistryFactory) createImmuRegistryDB() (RegistryDB, error) {
	// Get connection from database pool
	conn, err := database.GetOrCreateContractsDBPool(f.config)
	if err != nil {
		return nil, fmt.Errorf("failed to get contractsdb connection: %w", err)
	}

	return NewImmuRegistryDB(conn), nil
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
func (cf *ContextualRegistryFactory) CreateRegistryDB() (RegistryDB, error) {
	return cf.factory.CreateRegistryDB()
}
