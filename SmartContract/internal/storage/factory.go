package storage

import (
	"fmt"
	"gossipnode/SmartContract/internal/database"
)

// StorageType enumerates supported storage types
type StorageType string

const (
	StoreTypePebble StorageType = "pebble"
	StoreTypeMemory StorageType = "memory"
)

// Factory Config
type Config struct {
	Type StorageType
	Path string
}

// NewKVStore creates a new KVStore based on the configuration.
func NewKVStore(cfg Config) (KVStore, error) {
	switch cfg.Type {
	case StoreTypePebble:
		return NewPebbleStore(cfg.Path)
	case StoreTypeMemory:
		return nil, fmt.Errorf("memory store not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
	}
}

// Convert DBConfig to StorageConfig adapter
func ConfigFromEnv(dbConfig *database.Config) Config {
	// Simple mapping for now
	// Ideally we'd have a specific field in config, but for legacy compatibility we can infer
	// "ImmuDB" was the old default, we can map that to Pebble for now or use a new env var

	// Default to Pebble
	return Config{
		Type: StoreTypePebble,
		Path: "./contract_storage_pebble",
	}
}
