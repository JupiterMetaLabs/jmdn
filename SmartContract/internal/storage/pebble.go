package storage

import (
	"errors"

	"github.com/cockroachdb/pebble"
)

// PebbleStore implements KVStore using PebbleDB.
type PebbleStore struct {
	db *pebble.DB
}

// Ensure PebbleStore implements KVStore
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
			return nil, nil // Return nil for not found to match interface
		}
		return nil, err
	}
	// We must copy the value because closer.Close() invalidates the slice
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
	return &pebbleBatch{
		batch: s.db.NewBatch(),
	}
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

// pebbleBatch implements Batch
type pebbleBatch struct {
	batch *pebble.Batch
}

func (b *pebbleBatch) Set(key, value []byte) error {
	return b.batch.Set(key, value, nil)
}

func (b *pebbleBatch) Delete(key []byte) error {
	return b.batch.Delete(key, nil)
}

func (b *pebbleBatch) Commit() error {
	return b.batch.Commit(pebble.Sync)
}

func (b *pebbleBatch) Close() error {
	return b.batch.Close()
}

// pebbleIterator implements Iterator
type pebbleIterator struct {
	iter *pebble.Iterator
}

func (i *pebbleIterator) First() bool {
	return i.iter.First()
}

func (i *pebbleIterator) Next() bool {
	return i.iter.Next()
}

func (i *pebbleIterator) Key() []byte {
	return i.iter.Key()
}

func (i *pebbleIterator) Value() []byte {
	return i.iter.Value()
}

func (i *pebbleIterator) Close() error {
	return i.iter.Close()
}

// keyUpperBound returns the immediate next key for a prefix
func keyUpperBound(b []byte) []byte {
	end := make([]byte, len(b))
	copy(end, b)
	for i := len(end) - 1; i >= 0; i-- {
		end[i] = end[i] + 1
		if end[i] != 0 {
			return end
		}
	}
	return nil // overflow
}
