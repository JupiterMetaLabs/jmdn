package contractDB

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/cockroachdb/pebble"
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
	StoreTypePebble StoreType = "pebble"
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
	case StoreTypePebble:
		return NewPebbleStore(cfg.Path)
	case StoreTypeMemory:
		return NewMemKVStore(), nil
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
	}
}

// DefaultConfig returns a Config suitable for production use.
// The storage path can be overridden via the CONTRACT_DB_PATH environment variable.
func DefaultConfig() Config {
	path := "./contract_storage_pebble"
	if p := os.Getenv("CONTRACT_DB_PATH"); p != "" {
		path = p
	}
	return Config{
		Type: StoreTypePebble,
		Path: path,
	}
}

// ============================================================================
// PebbleStore — production KV store backed by CockroachDB Pebble
// ============================================================================

// PebbleStore implements KVStore using PebbleDB.
type PebbleStore struct {
	db *pebble.DB
}

// Ensure PebbleStore implements KVStore.
var _ KVStore = (*PebbleStore)(nil)

// NewPebbleStore opens a PebbleDB at the given path.
func NewPebbleStore(path string) (*PebbleStore, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, err
	}
	return &PebbleStore{db: db}, nil
}

func (s *PebbleStore) Get(key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil // nil means not-found (matches interface contract)
		}
		return nil, err
	}
	valCopy := make([]byte, len(val))
	copy(valCopy, val)
	closer.Close()
	return valCopy, nil
}

func (s *PebbleStore) Set(key, value []byte) error {
	return s.db.Set(key, value, pebble.Sync)
}

func (s *PebbleStore) Delete(key []byte) error {
	return s.db.Delete(key, pebble.Sync)
}

func (s *PebbleStore) NewBatch() Batch {
	return &pebbleBatch{batch: s.db.NewBatch()}
}

func (s *PebbleStore) Close() error {
	return s.db.Close()
}

func (s *PebbleStore) NewIterator(prefix []byte) (Iterator, error) {
	opts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		opts.LowerBound = prefix
		opts.UpperBound = keyUpperBound(prefix)
	}
	iter, err := s.db.NewIter(opts)
	if err != nil {
		return nil, err
	}
	return &pebbleIterator{iter: iter}, nil
}

// pebbleBatch implements Batch for PebbleDB.
type pebbleBatch struct {
	batch *pebble.Batch
}

func (b *pebbleBatch) Set(key, value []byte) error { return b.batch.Set(key, value, nil) }
func (b *pebbleBatch) Delete(key []byte) error      { return b.batch.Delete(key, nil) }
func (b *pebbleBatch) Commit() error                { return b.batch.Commit(pebble.Sync) }
func (b *pebbleBatch) Close() error                 { return b.batch.Close() }

// pebbleIterator implements Iterator for PebbleDB.
type pebbleIterator struct {
	iter *pebble.Iterator
}

func (i *pebbleIterator) First() bool  { return i.iter.First() }
func (i *pebbleIterator) Next() bool   { return i.iter.Next() }
func (i *pebbleIterator) Key() []byte  { return i.iter.Key() }
func (i *pebbleIterator) Value() []byte { return i.iter.Value() }
func (i *pebbleIterator) Close() error { return i.iter.Close() }

// keyUpperBound returns the immediate next key for a prefix scan.
func keyUpperBound(b []byte) []byte {
	end := make([]byte, len(b))
	copy(end, b)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end
		}
	}
	return nil // overflow
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
		return nil, errors.New("key not found")
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
