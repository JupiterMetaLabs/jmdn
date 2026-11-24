package messaging

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dgraph-io/badger/v4"

	"gossipnode/internal/ckptfloor"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/checkpoint"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
)

// chainHead reads (seq, head) from the store's chain head.
func chainHead(t *testing.T, s kv.Store) (uint64, []byte) {
	t.Helper()
	raw, err := s.Get([]byte(chainHeadKey))
	if err != nil {
		t.Fatal(err)
	}
	return binary.BigEndian.Uint64(raw[:8]), append([]byte(nil), raw[8:40]...)
}

// lightNode appends `blocks` blocks of recordsPerBlock records and, every
// `every` blocks, stores a checkpoint of the head (block number recorded) and
// raises the floor to it, the way the signing hook does.
func lightNode(t *testing.T, s kv.Store, floor string, blocks, recordsPerBlock, every int) []*checkpoint.Checkpoint {
	t.Helper()
	var cps []*checkpoint.Checkpoint
	for b := 1; b <= blocks; b++ {
		appendRecords(t, s, recordsPerBlock)
		if b%every != 0 {
			continue
		}
		seq, head := chainHead(t, s)
		c := &checkpoint.Checkpoint{ChainID: 7, Seq: seq, Head: head, Signature: []byte("sig"), BlockNumber: uint64(b)}
		if err := storeVerifiedCheckpoint(s, c, acceptAll, floor); err != nil {
			t.Fatal(err)
		}
		cps = append(cps, c)
	}
	return cps
}

func TestPickPruneCheckpoint(t *testing.T) {
	cps := []*checkpoint.Checkpoint{
		{Seq: 10, BlockNumber: 20},
		{Seq: 20, BlockNumber: 40},
		{Seq: 30, BlockNumber: 0}, // no block number: never picked
		{Seq: 40, BlockNumber: 80},
		{Seq: 50, BlockNumber: 100},
	}
	cases := []struct {
		head, keep, floor uint64
		want              uint64 // seq; 0 = none
	}{
		{head: 100, keep: 60, floor: 50, want: 20}, // cutoff 40
		{head: 100, keep: 20, floor: 50, want: 40}, // cutoff 80
		{head: 100, keep: 20, floor: 30, want: 20}, // floor stops at seq 30
		{head: 100, keep: 90, floor: 50, want: 0},  // cutoff 10: nothing old enough
		{head: 50, keep: 100, floor: 50, want: 0},  // head below keep
		{head: 100, keep: 0, floor: 5, want: 0},    // floor below every checkpoint
	}
	for i, c := range cases {
		got, ok := pickPruneCheckpoint(cps, c.head, c.keep, c.floor)
		switch {
		case c.want == 0 && ok:
			t.Errorf("case %d: picked seq %d, want none", i, got.Seq)
		case c.want != 0 && (!ok || got.Seq != c.want):
			t.Errorf("case %d: got %+v ok=%v, want seq %d", i, got, ok, c.want)
		}
	}
}

// A light node prunes below a checkpoint keepBlocks behind the head, never
// above its floor, and still passes the boot gate afterwards.
func TestPruneLightLogOnce_PrunesAndBootGatePasses(t *testing.T) {
	s := newCkptStore(t)
	floor := filepath.Join(t.TempDir(), "floor.json")
	cps := lightNode(t, s, floor, 100, 3, 10) // checkpoints at blocks 10..100

	res, err := PruneLightLogOnce(s, floor, 35)
	if err != nil {
		t.Fatal(err)
	}
	// head block 100, cutoff 65 → newest checkpoint at or below block 65 is block 60.
	if res.CutBlock != 60 || res.HeadBlock != 100 {
		t.Fatalf("result %+v, want cut at block 60", res)
	}
	want := cps[5].Seq + 1
	if res.Before != want || res.Deleted != int(want) {
		t.Fatalf("result %+v, want log start %d and %d deleted", res, want, want)
	}
	if start, _ := kv.LogStart(s); start != want {
		t.Fatalf("log start %d, want %d", start, want)
	}

	// The same pass again does nothing.
	if again, err := PruneLightLogOnce(s, floor, 35); err != nil || again.Deleted != 0 {
		t.Fatalf("second pass %+v err=%v", again, err)
	}

	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, floor, 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("boot gate on the pruned log: ok=%v err=%v", ok, err)
	}
	// A full walk is impossible now, and the background check says so.
	if ran, err := RunBackgroundFullVerify(context.Background(), s, acceptAll); ran || err != nil {
		t.Fatalf("background full verify on a pruned log: ran=%v err=%v", ran, err)
	}
	// Appends continue.
	appendRecords(t, s, 2)
}

// The floor caps the cut: a checkpoint above the floor is never pruned below.
func TestPruneLightLogOnce_RespectsFloor(t *testing.T) {
	s := newCkptStore(t)
	floor := filepath.Join(t.TempDir(), "floor.json")
	cps := lightNode(t, s, floor, 40, 2, 10) // checkpoints at blocks 10..40
	// Pretend the floor is still at the first checkpoint.
	lowFloor := filepath.Join(t.TempDir(), "low.json")
	if err := ckptfloor.Raise(lowFloor, 7, cps[0].Seq, cps[0].Head); err != nil {
		t.Fatal(err)
	}
	res, err := PruneLightLogOnce(s, lowFloor, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Before != cps[0].Seq+1 {
		t.Fatalf("pruned to %d, want %d (floor caps the cut)", res.Before, cps[0].Seq+1)
	}
}

func TestPruneLightLogOnce_NoFloorNoPrune(t *testing.T) {
	s := newCkptStore(t)
	lightNode(t, s, "", 30, 2, 10)
	res, err := PruneLightLogOnce(s, filepath.Join(t.TempDir(), "missing.json"), 5)
	if err != nil || res.Deleted != 0 {
		t.Fatalf("result %+v err=%v, want no prune without a floor", res, err)
	}
	if start, _ := kv.LogStart(s); start != 0 {
		t.Fatalf("log start %d, want 0", start)
	}
}

// A pruned log whose floor file is gone verifies from the oldest checkpoint
// the kept log covers.
func TestBootGatePrunedLogWithoutFloor(t *testing.T) {
	s := newCkptStore(t)
	floor := filepath.Join(t.TempDir(), "floor.json")
	lightNode(t, s, floor, 60, 2, 10)
	if _, err := PruneLightLogOnce(s, floor, 25); err != nil {
		t.Fatal(err)
	}
	ok, err := CheckpointBootGate(context.Background(), s, acceptAll, filepath.Join(t.TempDir(), "gone.json"), 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("pruned log, no floor: ok=%v err=%v", ok, err)
	}
}

// The boot gate reads only the log after the floor checkpoint; the
// background full walk is what catches a change below it.
func TestBackgroundFullVerifyCatchesChangeBelowFloor(t *testing.T) {
	dir := t.TempDir()
	s, err := kv.NewStore(kv.Config{Path: dir, NoSyncWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(t.TempDir(), "floor.json")
	lightNode(t, s, floor, 30, 2, 10)
	if ran, err := RunBackgroundFullVerify(context.Background(), s, acceptAll); !ran || err != nil {
		t.Fatalf("clean log: ran=%v err=%v", ran, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewrite record 3, far below the floor, directly in Badger.
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(fmt.Sprintf("__sys:log:%d", 3)), []byte(`{"Seq":3,"Value":"dGFtcGVyZWQ="}`))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = kv.NewStore(kv.Config{Path: dir, NoSyncWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if ok, err := CheckpointBootGate(context.Background(), s, acceptAll, floor, 0, 0, 0); err != nil || !ok {
		t.Fatalf("boot gate (suffix only) should pass: ok=%v err=%v", ok, err)
	}
	_, err = RunBackgroundFullVerify(context.Background(), s, acceptAll)
	if !errors.Is(err, checkpoint.ErrHeadMismatch) {
		t.Fatalf("background full verify: want ErrHeadMismatch, got %v", err)
	}
}
