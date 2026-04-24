package contractDB

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
)

// ============================================================================
// KVStore Interface
// ============================================================================

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

// ============================================================================
// Factory
// ============================================================================

// StoreType enumerates supported storage types.
type StoreType string

const (
	StoreTypeMemory StoreType = "memory"
)

// Config holds the configuration for creating a KVStore.
type Config struct {
	Type StoreType
	Path string
}

// NewKVStore creates a new KVStore based on the configuration.
func NewKVStore(cfg Config) (KVStore, error) {
	switch cfg.Type {
	case StoreTypeMemory:
		return NewMemKVStore(), nil
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
	}
}

// DefaultConfig returns a Config suitable for production use.
func DefaultConfig() Config {
	return Config{
		Type: StoreTypeMemory,
	}
}

// ============================================================================
// MemKVStore — in-memory KV store for testing
// ============================================================================

// MemKVStore is an in-memory key-value store intended for unit tests.
type MemKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemKVStore creates a new in-memory KV store.
func NewMemKVStore() *MemKVStore {
	return &MemKVStore{data: make(map[string][]byte)}
}

func (m *MemKVStore) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.data[string(key)]
	if !ok {
		return nil, nil // nil, nil matches the KVStore interface contract (not-found = nil, nil)
	}
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

func (m *MemKVStore) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *MemKVStore) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *MemKVStore) NewBatch() Batch {
	return &memBatch{
		store:       m,
		ops:         make(map[string]*batchOp),
		orderedKeys: []string{},
	}
}

func (m *MemKVStore) NewIterator(prefix []byte) (Iterator, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if bytes.HasPrefix([]byte(k), prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	data := make(map[string][]byte, len(keys))
	for _, k := range keys {
		data[k] = m.data[k]
	}
	return &memIterator{keys: keys, data: data, pos: -1}, nil
}

func (m *MemKVStore) Close() error { return nil }

type batchOp struct {
	value  []byte
	delete bool
}

type memBatch struct {
	store       *MemKVStore
	ops         map[string]*batchOp
	orderedKeys []string
	mu          sync.Mutex
}

func (b *memBatch) Set(key, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := string(key)
	cp := make([]byte, len(value))
	copy(cp, value)
	if _, exists := b.ops[k]; !exists {
		b.orderedKeys = append(b.orderedKeys, k)
	}
	b.ops[k] = &batchOp{value: cp}
	return nil
}

func (b *memBatch) Delete(key []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := string(key)
	if _, exists := b.ops[k]; !exists {
		b.orderedKeys = append(b.orderedKeys, k)
	}
	b.ops[k] = &batchOp{delete: true}
	return nil
}

func (b *memBatch) Commit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, k := range b.orderedKeys {
		op := b.ops[k]
		if op.delete {
			delete(b.store.data, k)
		} else {
			b.store.data[k] = op.value
		}
	}
	return nil
}

func (b *memBatch) Close() error { return nil }

type memIterator struct {
	keys []string
	data map[string][]byte
	pos  int
}

func (it *memIterator) First() bool {
	if len(it.keys) == 0 {
		return false
	}
	it.pos = 0
	return true
}

func (it *memIterator) Next() bool {
	it.pos++
	return it.pos < len(it.keys)
}

func (it *memIterator) Key() []byte {
	if it.pos < 0 || it.pos >= len(it.keys) {
		return nil
	}
	return []byte(it.keys[it.pos])
}

func (it *memIterator) Value() []byte {
	if it.pos < 0 || it.pos >= len(it.keys) {
		return nil
	}
	return it.data[it.keys[it.pos]]
}

func (it *memIterator) Close() error { return nil }
