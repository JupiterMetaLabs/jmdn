// Package storage re-exports all types from gossipnode/DB_OPs/contractDB.
// This file is a forwarding stub created during the contractDB migration.
// All callers should migrate their imports to gossipnode/DB_OPs/contractDB directly.
// This stub will be deleted once all callers have been updated (commit 15).
package storage

import (
	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/database"
)

// ---- Type aliases ----

type KVStore = contractDB.KVStore
type Batch = contractDB.Batch
type Iterator = contractDB.Iterator
type StorageType = contractDB.StoreType
type Config = contractDB.Config

// ---- Constants ----

const (
	StoreTypePebble = contractDB.StoreTypePebble
	StoreTypeMemory = contractDB.StoreTypeMemory
)

// ---- Constructor / factory aliases ----

var NewKVStore = contractDB.NewKVStore
var NewMemKVStore = contractDB.NewMemKVStore
var NewPebbleStore = contractDB.NewPebbleStore

// ConfigFromEnv creates a storage Config from a database.Config.
// Kept for backward compatibility — always returns a Pebble config.
func ConfigFromEnv(_ *database.Config) Config {
	return contractDB.DefaultConfig()
}
