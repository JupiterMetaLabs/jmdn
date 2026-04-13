package repository

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// StorageMetadata tracks when a storage slot was last modified
type StorageMetadata struct {
	ContractAddress   common.Address `json:"contract_address"`
	StorageKey        common.Hash    `json:"storage_key"`
	ValueHash         common.Hash    `json:"value_hash"`
	LastModifiedBlock uint64         `json:"last_modified_block"`
	LastModifiedTx    common.Hash    `json:"last_modified_tx"`
	UpdatedAt         int64          `json:"updated_at"`
}

// StateRepository is the generic interface that any underlying database
// (Immudb, PostgreSQL, Pebble) must implement to store smart contract state.
// This design enables an easy "Dual-Write" migration to SQL in the future.
type StateRepository interface {
	// ==========================================
	// Transactional Control
	// ==========================================
	// NewBatch starts a new atomic batch operation.
	// Returns a transactional object that must be Committed or Discarded.
	NewBatch() StateBatch

	// ==========================================
	// Direct Read Operations
	// ==========================================

	// Code
	GetCode(ctx context.Context, addr common.Address) ([]byte, error)

	// Storage
	GetStorage(ctx context.Context, addr common.Address, key common.Hash) (common.Hash, error)
	GetStorageMetadata(ctx context.Context, addr common.Address, key common.Hash) (*StorageMetadata, error)

	// Nonce
	GetNonce(ctx context.Context, addr common.Address) (uint64, error)

	// ==========================================
	// Metadata & Receipts
	// ==========================================
	GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error)
	GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error)
}

// StateBatch represents an atomic set of writes to the StateRepository.
// For KV stores, this wraps WriteBatch. For SQL, this wraps a DB Transaction.
type StateBatch interface {
	SaveCode(addr common.Address, code []byte) error
	DeleteCode(addr common.Address) error

	SaveStorage(addr common.Address, key common.Hash, value common.Hash) error
	DeleteStorage(addr common.Address, key common.Hash) error

	SaveStorageMetadata(addr common.Address, key common.Hash, meta StorageMetadata) error
	DeleteStorageMetadata(addr common.Address, key common.Hash) error

	SaveNonce(addr common.Address, nonce uint64) error
	DeleteNonce(addr common.Address) error

	SaveContractMetadata(addr common.Address, data []byte) error
	SaveReceipt(txHash common.Hash, data []byte) error

	// Commit applies all the staged writes atomically.
	Commit() error
	// Close discards the batch if Commit hasn't been called.
	Close() error
}
