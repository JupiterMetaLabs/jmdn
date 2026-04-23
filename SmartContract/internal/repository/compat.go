// Package repository re-exports all types from gossipnode/DB_OPs/contractDB.
// This file is a forwarding stub created during the contractDB migration.
// All callers should migrate their imports to gossipnode/DB_OPs/contractDB directly.
// This stub will be deleted once all callers have been updated (commit 14).
package repository

import (
	contractDB "gossipnode/DB_OPs/contractDB"
)

// ---- Type aliases ----

type StateRepository = contractDB.StateRepository
type StateBatch = contractDB.StateBatch
type StorageMetadata = contractDB.StorageMetadata

// ---- Constructor aliases ----

var NewPebbleAdapter = contractDB.NewPebbleAdapter
