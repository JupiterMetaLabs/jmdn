package storage

import (
	"bytes"
	"errors"
	"sort"
	"sync"
)

// MemKVStore is an in-memory key-value store for testing
type MemKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemKVStore creates a new in-memory KV store
func NewMemKVStore() *MemKVStore {
	return &MemKVStore{
		data: make(map[string][]byte),
	}
}

func (m *MemKVStore) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	val, ok := m.data[string(key)]
	if !ok {
		return nil, errors.New("key not found")
	}

	// Return copy to avoid mutation
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

func (m *MemKVStore) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store copy to avoid external mutation
	valCopy := make([]byte, len(value))
	copy(valCopy, value)
	m.data[string(key)] = valCopy
	return nil
}

func (m *MemKVStore) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, string(key))
	return nil
}

func (m *MemKVStore) NewBatch() Batch {
	return &MemBatch{
		store:       m,
		ops:         make(map[string]*batchOp),
		orderedKeys: []string{},
	}
}

func (m *MemKVStore) NewIterator(prefix []byte) (Iterator, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect matching keys
	var keys []string
	prefixStr := string(prefix)
	for k := range m.data {
		if bytes.HasPrefix([]byte(k), prefix) {
			keys = append(keys, k)
		}
	}

	sort.Strings(keys)

	// Copy data for iterator
	data := make(map[string][]byte)
	for _, k := range keys {
		data[k] = m.data[k]
	}

	return &MemIterator{
		prefix: prefixStr,
		keys:   keys,
		data:   data,
		pos:    -1,
	}, nil
}

func (m *MemKVStore) Close() error {
	return nil
}

// batchOp represents a single operation in a batch
type batchOp struct {
	value  []byte
	delete bool
}

// MemBatch implements Batch for in-memory store
type MemBatch struct {
	store       *MemKVStore
	ops         map[string]*batchOp
	orderedKeys []string
	mu          sync.Mutex
}

func (b *MemBatch) Set(key, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	keyStr := string(key)
	valCopy := make([]byte, len(value))
	copy(valCopy, value)

	if _, exists := b.ops[keyStr]; !exists {
		b.orderedKeys = append(b.orderedKeys, keyStr)
	}

	b.ops[keyStr] = &batchOp{
		value:  valCopy,
		delete: false,
	}
	return nil
}

func (b *MemBatch) Delete(key []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	keyStr := string(key)
	if _, exists := b.ops[keyStr]; !exists {
		b.orderedKeys = append(b.orderedKeys, keyStr)
	}

	b.ops[keyStr] = &batchOp{
		delete: true,
	}
	return nil
}

func (b *MemBatch) Commit() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.store.mu.Lock()
	defer b.store.mu.Unlock()

	for _, keyStr := range b.orderedKeys {
		op := b.ops[keyStr]
		if op.delete {
			delete(b.store.data, keyStr)
		} else {
			b.store.data[keyStr] = op.value
		}
	}

	return nil
}

func (b *MemBatch) Close() error {
	return nil
}

// MemIterator implements Iterator for in-memory store
type MemIterator struct {
	prefix string
	keys   []string
	data   map[string][]byte
	pos    int
}

func (it *MemIterator) First() bool {
	if len(it.keys) == 0 {
		return false
	}
	it.pos = 0
	return true
}

func (it *MemIterator) Next() bool {
	it.pos++
	return it.pos < len(it.keys)
}

func (it *MemIterator) Key() []byte {
	if it.pos < 0 || it.pos >= len(it.keys) {
		return nil
	}
	return []byte(it.keys[it.pos])
}

func (it *MemIterator) Value() []byte {
	if it.pos < 0 || it.pos >= len(it.keys) {
		return nil
	}
	return it.data[it.keys[it.pos]]
}

func (it *MemIterator) Close() error {
	return nil
}
