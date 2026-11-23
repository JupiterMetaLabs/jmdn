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
	"strings"
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
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
	// RoundID identifies the certified round: the block's ConsensusHash,
	// which covers its period. BlockHash covers only the transactions, so the
	// same transactions certified again at a later period share it. Empty in
	// records written before this field existed.
	RoundID   string          `json:"round_id,omitempty"`
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
		return fmt.Errorf("%w: decode %s: %v", ErrUnreadable, t.key, err)
	}
	t.rec = &r
	t.loaded = true
	return nil
}

// ErrDropAtPeriodCap is returned when a pending record cannot be dropped
// because its block is already at the period cap: no new round could start
// at the height, so the same transactions proposed again would rebuild the
// dropped round.
var ErrDropAtPeriodCap = errors.New("pendingcommit: the pending block is at the period cap; dropping it could not start a new round at its height")

// ErrUnreadable is returned (wrapped) while the stored record cannot be
// decoded. Proposals stay refused (fail closed) until an operator removes it
// with DropUnreadable.
var ErrUnreadable = errors.New("pendingcommit: stored record cannot be decoded")

// ErrReadable is returned by DropUnreadable when the stored record decodes:
// a readable record is dropped only with DropMatching.
var ErrReadable = errors.New("pendingcommit: stored record is readable; drop it by height and block hash")

// DropUnreadable removes the stored record only when it cannot be decoded
// (operator last resort). A readable record is refused with ErrReadable, and
// an empty store with ErrNoPending.
func (t *Tracker) DropUnreadable() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.store == nil {
		if t.rec == nil {
			return ErrNoPending
		}
		return ErrReadable
	}
	raw, err := t.store.Get(t.key)
	if err != nil {
		return fmt.Errorf("pendingcommit: read %s: %w", t.key, err)
	}
	if len(raw) == 0 {
		return ErrNoPending
	}
	var r Record
	if json.Unmarshal(raw, &r) == nil {
		return ErrReadable
	}
	if err := t.store.Put(t.key, []byte{}); err != nil {
		return fmt.Errorf("pendingcommit: drop %s: %w", t.key, err)
	}
	t.rec = nil
	t.loaded = true
	return nil
}

// Save records rec as the pending block. The record is kept in memory even
// when the store write fails, so this process still refuses new proposals at
// the height; the error tells the caller the record will not survive a
// restart. Saving a record for a different height while one is pending is
// refused.
func (t *Tracker) Save(rec Record) error {
	return t.save(rec, 0)
}

// SaveOver is Save, except that a pending record at or below committedTip
// (a height the chain has already reached, whose record could not be
// settled) is replaced instead of refusing rec. That old record no longer
// blocks proposals, so refusing rec would leave rec's height unprotected.
func (t *Tracker) SaveOver(rec Record, committedTip uint64) error {
	return t.save(rec, committedTip)
}

func (t *Tracker) save(rec Record, committedTip uint64) error {
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
	replaceable := t.rec != nil && t.rec.Height <= committedTip && rec.Height > committedTip
	if t.rec != nil && (t.rec.Height != rec.Height || t.rec.BlockHash != rec.BlockHash) && !replaceable {
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

// Clear removes the record for height, blockHash and roundID after the block
// was stored (or an operator dropped it). Clearing a different height, block
// or round, or when nothing is pending, is a no-op: a commit of one block can
// never remove a record saved for another block, or for the same
// transactions certified again in a later round, at the same height. An empty
// roundID (on either side) matches any round.
func (t *Tracker) Clear(height uint64, blockHash, roundID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return err
	}
	if t.rec == nil || t.rec.Height != height || !strings.EqualFold(t.rec.BlockHash, blockHash) {
		return nil
	}
	if roundID != "" && t.rec.RoundID != "" && !strings.EqualFold(t.rec.RoundID, roundID) {
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

// ErrNoPending is returned by DropMatching when no record is pending.
var ErrNoPending = errors.New("pendingcommit: no pending certified block")

// ErrRecordMismatch is returned by DropMatching when height or block hash
// does not match the pending record.
var ErrRecordMismatch = errors.New("pendingcommit: height and block hash do not match the pending record")

// DropMatching removes the pending record if, and only if, its height and
// block hash match, and returns the removed record. The check and the removal
// happen under one lock. On a mismatch the current record is returned with
// ErrRecordMismatch.
func (t *Tracker) DropMatching(height uint64, blockHash string) (*Record, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	if t.rec == nil {
		return nil, ErrNoPending
	}
	if t.rec.Height != height || !strings.EqualFold(t.rec.BlockHash, strings.TrimSpace(blockHash)) {
		r := *t.rec
		return &r, ErrRecordMismatch
	}
	if t.store != nil {
		if err := t.store.Put(t.key, []byte{}); err != nil {
			return nil, fmt.Errorf("pendingcommit: drop %s: %w", t.key, err)
		}
	}
	r := *t.rec
	t.rec = nil
	return &r, nil
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
