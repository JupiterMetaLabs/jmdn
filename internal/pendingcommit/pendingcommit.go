// Package pendingcommit holds the sequencer's record of a certified block that
// has not been stored yet.
//
// Once a block has a valid certificate, the sequencer must either commit that
// block or halt the height. It must never move to a new period and certify a
// different block at the same height, because the first certificate still
// exists. The record is written before the first apply attempt and removed only
// after the block is stored, so it survives a restart:
//
//   - the block server refuses new proposals at or above the pending height;
//   - the operator round-ledger clear refuses the pending height;
//   - startup resumes committing the recorded block.
//
// The record is local to the sequencer. It is never served, gossiped or synced.
package pendingcommit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Store is the durable key-value store behind a Tracker. Get returns (nil, nil)
// when the key is absent; Put with an empty value deletes it.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
}

// Record is one certified, uncommitted block.
type Record struct {
	Height    uint64          `json:"height"`
	BlockHash string          `json:"block_hash"`
	Block     json.RawMessage `json:"block"`
	Results   json.RawMessage `json:"results"`
	CreatedAt int64           `json:"created_at"`
}

// DefaultKey is the store key of the single pending record.
const DefaultKey = "pendingcommit:record"

// Tracker keeps at most one pending record. One is enough: while a record
// exists, the block server admits no proposal at or above its height.
type Tracker struct {
	mu     sync.Mutex
	store  Store
	key    string
	rec    *Record
	loaded bool
}

// New returns a Tracker with no store (memory only).
func New() *Tracker { return &Tracker{key: DefaultKey} }

// Default is the process-wide tracker used by the sequencer.
var Default = New()

// SetStore attaches the durable store. Call once at startup, before any block
// is processed; the record is loaded lazily on first use.
func (t *Tracker) SetStore(s Store, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store = s
	if key != "" {
		t.key = key
	}
	t.rec = nil
	t.loaded = false
}

func (t *Tracker) loadLocked() error {
	if t.loaded {
		return nil
	}
	if t.store == nil {
		t.loaded = true
		return nil
	}
	raw, err := t.store.Get(t.key)
	if err != nil {
		return fmt.Errorf("pendingcommit: read %s: %w", t.key, err)
	}
	if len(raw) == 0 {
		t.rec = nil
		t.loaded = true
		return nil
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("pendingcommit: decode %s: %w", t.key, err)
	}
	t.rec = &r
	t.loaded = true
	return nil
}

// Save records rec as the pending block. The record is kept in memory even
// when the store write fails, so this process still refuses new proposals at
// the height; the error tells the caller the record will not survive a
// restart. Saving a record for a different height while one is pending is
// refused.
func (t *Tracker) Save(rec Record) error {
	if rec.Height == 0 || rec.BlockHash == "" || len(rec.Block) == 0 {
		return errors.New("pendingcommit: record needs a height, a block hash and a block")
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = time.Now().Unix()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		// Keep the new record in memory anyway: refusing proposals is the safe side.
		r := rec
		t.rec = &r
		t.loaded = true
		return err
	}
	if t.rec != nil && (t.rec.Height != rec.Height || t.rec.BlockHash != rec.BlockHash) {
		return fmt.Errorf("pendingcommit: height %d (%s) is already pending; refusing to record height %d (%s)",
			t.rec.Height, t.rec.BlockHash, rec.Height, rec.BlockHash)
	}
	r := rec
	t.rec = &r
	if t.store == nil {
		return nil
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("pendingcommit: encode: %w", err)
	}
	if err := t.store.Put(t.key, raw); err != nil {
		return fmt.Errorf("pendingcommit: write %s: %w", t.key, err)
	}
	return nil
}

// Current returns the pending record, or nil when none exists. A store read
// error is returned so callers can fail closed.
func (t *Tracker) Current() (*Record, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	if t.rec == nil {
		return nil, nil
	}
	r := *t.rec
	return &r, nil
}

// Clear removes the record for height after the block was stored. Clearing a
// different height, or when nothing is pending, is a no-op.
func (t *Tracker) Clear(height uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return err
	}
	if t.rec == nil || t.rec.Height != height {
		return nil
	}
	if t.store != nil {
		if err := t.store.Put(t.key, []byte{}); err != nil {
			return fmt.Errorf("pendingcommit: clear %s: %w", t.key, err)
		}
	}
	t.rec = nil
	return nil
}

// ClearStale drops a record at or below committedTip: its block is already
// stored, but an earlier Clear failed. Without this the stale record would make
// Save refuse the next certified height, leaving that height unprotected. The
// record is dropped from memory even when the store delete fails (the error is
// returned); the next Save overwrites the single store key anyway, and a stale
// record left in the store never blocks proposals. A record above
// committedTip is kept.
func (t *Tracker) ClearStale(committedTip uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return err
	}
	if t.rec == nil || t.rec.Height > committedTip {
		return nil
	}
	stale := t.rec.Height
	t.rec = nil
	if t.store != nil {
		if err := t.store.Put(t.key, []byte{}); err != nil {
			return fmt.Errorf("pendingcommit: clear stale record for height %d: %w", stale, err)
		}
	}
	return nil
}

// BlocksProposal reports whether a proposal at height must be refused because
// a certified block below or at it is still uncommitted. committedTip is the
// node's committed tip: a record at or below it is stale (the block was
// stored but the record was not cleared) and does not block. A store read
// error blocks (fail closed).
func (t *Tracker) BlocksProposal(height, committedTip uint64) (bool, *Record, error) {
	rec, err := t.Current()
	if err != nil {
		return true, nil, err
	}
	if rec == nil || rec.Height <= committedTip || height < rec.Height {
		return false, rec, nil
	}
	return true, rec, nil
}

// PendingAt reports whether height has a pending record. A store read error
// reports true (fail closed).
func (t *Tracker) PendingAt(height uint64) (bool, error) {
	rec, err := t.Current()
	if err != nil {
		return true, err
	}
	return rec != nil && rec.Height == height, nil
}
