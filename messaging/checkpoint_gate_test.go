package messaging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gossipnode/internal/ckptfloor"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/checkpoint"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
)

func newCkptStore(t *testing.T) kv.Store {
	t.Helper()
	s, err := kv.NewStore(kv.Config{Path: t.TempDir(), NoSyncWrites: true})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// appendRecords appends n records and returns the last sequence.
func appendRecords(t *testing.T, s kv.Store, n int) uint64 {
	t.Helper()
	var last uint64
	for i := 0; i < n; i++ {
		seq, err := s.Append(&core.CanonicalRecord{Namespace: "t", Type: "t", Value: []byte{byte(i)}, Timestamp: uint64(i)})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		last = seq
	}
	return last
}

func checkpointAt(t *testing.T, s kv.Store, seq uint64) *checkpoint.Checkpoint {
	t.Helper()
	head, err := checkpoint.RecomputeHead(context.Background(), s, 0, seq, make([]byte, 32))
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	return &checkpoint.Checkpoint{ChainID: 7, Seq: seq, Head: head, Signature: []byte("sig")}
}

func acceptAll(_, _ []byte, _ uint64) error { return nil }

// A checkpoint the node cannot verify must not be stored: the next boot would
// find it, fail, and refuse to start.
func TestStoreVerifiedCheckpointRefusesUnverifiable(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 3)
	floor := filepath.Join(t.TempDir(), "floor.json")
	reject := func(_, _ []byte, _ uint64) error { return errors.New("not in committee") }

	if err := storeVerifiedCheckpoint(s, checkpointAt(t, s, seq), reject, floor); err == nil {
		t.Fatal("expected an error")
	}
	if _, ok, _ := checkpoint.Latest(s); ok {
		t.Fatal("unverifiable checkpoint was stored")
	}
	if _, ok, _ := ckptfloor.Load(floor); ok {
		t.Fatal("floor raised for an unverifiable checkpoint")
	}
}

func TestStoreVerifiedCheckpointRaisesFloor(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 3)
	floor := filepath.Join(t.TempDir(), "floor.json")
	if err := storeVerifiedCheckpoint(s, checkpointAt(t, s, seq), acceptAll, floor); err != nil {
		t.Fatal(err)
	}
	f, ok, err := ckptfloor.Load(floor)
	if err != nil || !ok || f.Seq != seq || f.ChainID != 7 {
		t.Fatalf("floor = %+v ok=%v err=%v", f, ok, err)
	}
}

func TestBootGateNoCheckpointNoFloorPasses(t *testing.T) {
	s := newCkptStore(t)
	appendRecords(t, s, 2)
	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, filepath.Join(t.TempDir(), "floor.json"), 0, 0, 0)
	if err != nil || ok {
		t.Fatalf("got ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestBootGateVerifiesAndRaisesFloor(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 4)
	if err := checkpoint.Store(s, checkpointAt(t, s, seq)); err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(t.TempDir(), "floor.json")
	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, floor, 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("got ok=%v err=%v", ok, err)
	}
	if f, has, _ := ckptfloor.Load(floor); !has || f.Seq != seq {
		t.Fatalf("floor not raised to %d: %+v", seq, f)
	}
}

// The store's checkpoints are gone (keys deleted, or a store that never had
// them swapped in) while the floor file remembers one: boot must fail.
func TestBootGateDeletedCheckpointsFail(t *testing.T) {
	signed := newCkptStore(t)
	seq := appendRecords(t, signed, 4)
	floor := filepath.Join(t.TempDir(), "floor.json")
	if err := storeVerifiedCheckpoint(signed, checkpointAt(t, signed, seq), acceptAll, floor); err != nil {
		t.Fatal(err)
	}

	stripped := newCkptStore(t) // same records, no checkpoints
	appendRecords(t, stripped, 4)
	if ok, err := CheckpointBootGate(context.Background(), stripped, acceptAll, floor, 0, 0, 0); err == nil || ok {
		t.Fatalf("expected failure, got ok=%v err=%v", ok, err)
	}
}

// A store restored to an older state verifies on its own; only the floor file
// (kept outside it) shows that a newer checkpoint existed.
func TestBootGateRollbackFails(t *testing.T) {
	old := newCkptStore(t)
	oldSeq := appendRecords(t, old, 3)
	if err := checkpoint.Store(old, checkpointAt(t, old, oldSeq)); err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(t.TempDir(), "floor.json")
	// The node had later verified a checkpoint further along.
	if err := ckptfloor.Raise(floor, 7, oldSeq+5, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckpointBootGate(context.Background(), old, acceptAll, floor, 0, 0, 0); err == nil || ok {
		t.Fatalf("expected rollback to fail, got ok=%v err=%v", ok, err)
	}
}

func TestBootGateMinSeqWithoutCheckpointFails(t *testing.T) {
	s := newCkptStore(t)
	appendRecords(t, s, 2)
	_, err := CheckpointBootGate(context.Background(), s, acceptAll, "", 1, 0, 0)
	if !errors.Is(err, checkpoint.ErrNoCheckpoint) {
		t.Fatalf("expected ErrNoCheckpoint, got %v", err)
	}
}

// An unavailable committee source is retried before the result is returned.
func TestBootGateRetriesUnavailableCommittee(t *testing.T) {
	s := newCkptStore(t)
	seq := appendRecords(t, s, 2)
	if err := checkpoint.Store(s, checkpointAt(t, s, seq)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	flaky := func(_, _ []byte, _ uint64) error {
		calls++
		if calls < 3 {
			return fmt.Errorf("%w: seed down", ErrCheckpointCommitteeUnavailable)
		}
		return nil
	}
	ok, err := CheckpointBootGate(context.Background(), s, flaky, "", 0, 5, time.Millisecond)
	if err != nil || !ok || calls != 3 {
		t.Fatalf("got ok=%v err=%v calls=%d", ok, err, calls)
	}
	calls = 0
	if _, err := CheckpointBootGate(context.Background(), s, flaky, "", 0, 1, time.Millisecond); !errors.Is(err, ErrCheckpointCommitteeUnavailable) {
		t.Fatalf("expected unavailable after retries ran out, got %v", err)
	}
}
