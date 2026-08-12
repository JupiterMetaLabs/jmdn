// Package state re-exports all types from gossipnode/DB_OPs/contractDB.
// This file is a forwarding stub created during the contractDB migration.
// All callers should migrate their imports to gossipnode/DB_OPs/contractDB directly.
// This stub will be deleted once all callers have been updated (commit 13).
package state

import (
	contractDB "gossipnode/DB_OPs/contractDB"
)

// ---- Type aliases ----

type StateDB = contractDB.StateDB
type ContractDB = contractDB.ContractDB
type AccountData = contractDB.AccountData
type ContractMetadata = contractDB.ContractMetadata
type TransactionReceipt = contractDB.TransactionReceipt
type StorageMetadata = contractDB.StorageMetadata

// ---- Constructor aliases ----

var NewContractDB = contractDB.NewContractDB
var NewAccountData = contractDB.NewAccountData
