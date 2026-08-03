package contractDB

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// StateRepository is the generic interface any underlying database must implement
// to store smart contract state (code, storage, nonce, metadata, receipts).
// The design supports future "Dual-Write" migrations to SQL without touching callers.
type StateRepository interface {
	// NewBatch starts a new atomic write batch.
	NewBatch() StateBatch

	// GetCode returns the bytecode stored for addr.
	GetCode(ctx context.Context, addr common.Address) ([]byte, error)

	// GetStorage returns the value stored in a specific storage slot.
	GetStorage(ctx context.Context, addr common.Address, key common.Hash) (common.Hash, error)

	// GetStorageMetadata returns metadata for a storage slot.
	GetStorageMetadata(ctx context.Context, addr common.Address, key common.Hash) (*StorageMetadata, error)

	// GetNonce returns the locally cached nonce for addr.
	GetNonce(ctx context.Context, addr common.Address) (uint64, error)

	// GetContractMetadata returns the raw-encoded metadata for a deployed contract.
	GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error)

	// GetReceipt returns the raw-encoded receipt for a transaction.
	GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error)
}

// StateBatch represents an atomic set of writes to the StateRepository.
// For KV stores this wraps a WriteBatch; for SQL this wraps a DB transaction.
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

	// Commit applies all staged writes atomically.
	Commit() error
	// Close discards the batch if Commit has not been called.
	Close() error
}
