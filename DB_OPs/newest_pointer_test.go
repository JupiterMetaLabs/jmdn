package DB_OPs

// D-45: RecordBeaconEntropy and RecordVDFProof advance their "newest epoch"
// pointer with an unlocked read-compare-write. Two writers for epochs 9 and 10
// could both read the old pointer, 10 write first and 9 overwrite it, leaving
// the pointer at 9 with a record at 10. Restore probes down from the pointer
// and there is no prefix scan to fall back on, so epoch 10 became
// unreachable. These tests force that interleaving.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// racingPointerKV is mixKVHandle with the D-45 interleaving forced on one
// pointer key: each pointer read waits (briefly) for a second reader, and the
// write of 9 waits (briefly) for the write of 10. With the read-compare-write
// serialised the second reader never arrives in time and both waits just
// expire.
type racingPointerKV struct {
	*mixKVHandle
	pointerKey string

	mu       sync.Mutex
	readers  int
	bothRead chan struct{}
	wrote10  chan struct{}
}

const racingWait = 300 * time.Millisecond

func newRacingPointerKV(t *testing.T, pointerKey string) *racingPointerKV {
	t.Helper()
	h := &racingPointerKV{
		mixKVHandle: &mixKVHandle{kv: map[string][]byte{}},
		pointerKey:  pointerKey,
		bothRead:    make(chan struct{}),
		wrote10:     make(chan struct{}),
	}
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(nil) })
	return h
}

func (h *racingPointerKV) GetSyncKV(key string) ([]byte, error) {
	v, err := h.mixKVHandle.GetSyncKV(key)
	if key != h.pointerKey {
		return v, err
	}
	h.mu.Lock()
	h.readers++
	if h.readers == 2 {
		close(h.bothRead)
	}
	h.mu.Unlock()
	select {
	case <-h.bothRead:
	case <-time.After(racingWait):
	}
	return v, err
}

func (h *racingPointerKV) PutSyncKV(key string, value []byte) error {
	if key == h.pointerKey {
		var epoch uint64
		_ = json.Unmarshal(value, &epoch)
		if epoch == 9 {
			select {
			case <-h.wrote10:
			case <-time.After(racingWait):
			}
		}
		defer func() {
			if epoch == 10 {
				close(h.wrote10)
			}
		}()
	}
	return h.mixKVHandle.PutSyncKV(key, value)
}

func (h *racingPointerKV) pointer(t *testing.T) uint64 {
	t.Helper()
	raw, err := h.mixKVHandle.GetSyncKV(h.pointerKey)
	if err != nil {
		t.Fatalf("pointer %s not written: %v", h.pointerKey, err)
	}
	var newest uint64
	if err := json.Unmarshal(raw, &newest); err != nil {
		t.Fatal(err)
	}
	return newest
}

func writeConcurrently(t *testing.T, write func(epoch uint64) error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, epoch := range []uint64{9, 10} {
		wg.Add(1)
		go func(i int, epoch uint64) {
			defer wg.Done()
			errs[i] = write(epoch)
		}(i, epoch)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordBeaconEntropy_NewestPointerNeverMovesBackwards(t *testing.T) {
	h := newRacingPointerKV(t, beaconEntropyNewestKey)
	writeConcurrently(t, func(epoch uint64) error {
		return RecordBeaconEntropy(nil, epoch, []byte{byte(epoch), 1, 2, 3})
	})
	if got := h.pointer(t); got != 10 {
		t.Fatalf("newest pointer = %d after writing epochs 9 and 10, want 10: restore can no longer find epoch 10", got)
	}
}

func TestRecordVDFProof_NewestPointerNeverMovesBackwards(t *testing.T) {
	h := newRacingPointerKV(t, vdfProofNewestKey)
	writeConcurrently(t, func(epoch uint64) error {
		return RecordVDFProof(nil, epoch, []byte{byte(epoch), 1, 2, 3})
	})
	if got := h.pointer(t); got != 10 {
		t.Fatalf("newest pointer = %d after writing epochs 9 and 10, want 10", got)
	}
}
