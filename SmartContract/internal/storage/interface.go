package storage

// KVStore defines the interface for a key-value store.
type KVStore interface {
	// Get retrieves the value for a key. Returns nil if not found.
	Get(key []byte) ([]byte, error)
	// Set sets the value for a key.
	Set(key, value []byte) error
	// Delete removes a key.
	Delete(key []byte) error
	// NewBatch creates a new batch for atomic updates.
	NewBatch() Batch
	// NewIterator creates a new iterator for scanning.
	NewIterator(prefix []byte) (Iterator, error)
	// Close closes the store.
	Close() error
}

// Batch defines the interface for a batch of updates.
type Batch interface {
	// Set adds a set operation to the batch.
	Set(key, value []byte) error
	// Delete adds a delete operation to the batch.
	Delete(key []byte) error
	// Commit commits the batch to the store.
	Commit() error
	// Close closes the batch resources.
	Close() error
}

// Iterator defines the interface for iterating over keys.
type Iterator interface {
	// First moves to the first key.
	First() bool
	// Next moves to the next key.
	Next() bool
	// Key returns the current key.
	Key() []byte
	// Value returns the current value.
	Value() []byte
	// Close closes the iterator.
	Close() error
}
