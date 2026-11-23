package pendingcommit

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type memStore struct {
	mu      sync.Mutex
	kv      map[string][]byte
	failPut bool
	failGet bool
}

func newMemStore() *memStore { return &memStore{kv: map[string][]byte{}} }

func (m *memStore) Get(k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return nil, errors.New("get failed")
	}
	v, ok := m.kv[k]
	if !ok || len(v) == 0 {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (m *memStore) Put(k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return errors.New("put failed")
	}
	m.kv[k] = append([]byte(nil), v...)
	return nil
}

func rec(h uint64, hash string) Record {
	return Record{Height: h, BlockHash: hash, Block: json.RawMessage(`{"blocknumber":1}`), Results: json.RawMessage(`[]`)}
}

func TestSaveSurvivesRestart(t *testing.T) {
	s := newMemStore()
	a := New()
	a.SetStore(s, "")
	if err := a.Save(rec(10, "0xaa")); err != nil {
		t.Fatal(err)
	}
	b := New() // a restarted process
	b.SetStore(s, "")
	got, err := b.Current()
	if err != nil || got == nil || got.Height != 10 || got.BlockHash != "0xaa" {
		t.Fatalf("restart must reload the pending record, got %+v err %v", got, err)
	}
}

func TestClearRemovesRecordDurably(t *testing.T) {
	s := newMemStore()
	a := New()
	a.SetStore(s, "")
	_ = a.Save(rec(10, "0xaa"))
	if err := a.Clear(10, "0xaa"); err != nil {
		t.Fatal(err)
	}
	b := New()
	b.SetStore(s, "")
	if got, _ := b.Current(); got != nil {
		t.Fatalf("cleared record must not come back after restart, got %+v", got)
	}
}

func TestClearOtherHeightIsNoop(t *testing.T) {
	a := New()
	_ = a.Save(rec(10, "0xaa"))
	_ = a.Clear(9, "0xaa")
	if got, _ := a.Current(); got == nil {
		t.Fatal("clearing a different height must keep the pending record")
	}
}

func TestSecondHeightRefusedWhilePending(t *testing.T) {
	a := New()
	_ = a.Save(rec(10, "0xaa"))
	if err := a.Save(rec(11, "0xbb")); err == nil {
		t.Fatal("a second pending height must be refused")
	}
	if err := a.Save(rec(10, "0xcc")); err == nil {
		t.Fatal("a different block at the pending height must be refused")
	}
	if err := a.Save(rec(10, "0xaa")); err != nil {
		t.Fatalf("re-saving the same record must succeed: %v", err)
	}
}

func TestStoreWriteFailureStillBlocksInMemory(t *testing.T) {
	s := newMemStore()
	s.failPut = true
	a := New()
	a.SetStore(s, "")
	if err := a.Save(rec(10, "0xaa")); err == nil {
		t.Fatal("a failed store write must be reported")
	}
	blocked, _, _ := a.BlocksProposal(10, 9)
	if !blocked {
		t.Fatal("the record must still block proposals in this process after a failed write")
	}
}

func TestBlocksProposal(t *testing.T) {
	a := New()
	if blocked, _, _ := a.BlocksProposal(10, 9); blocked {
		t.Fatal("nothing pending must not block")
	}
	_ = a.Save(rec(10, "0xaa"))
	cases := []struct {
		height, tip uint64
		want        bool
	}{
		{10, 9, true},   // the pending height itself
		{11, 9, true},   // above it
		{9, 8, false},   // below it
		{11, 10, false}, // stale: height 10 already committed
	}
	for _, c := range cases {
		if got, _, _ := a.BlocksProposal(c.height, c.tip); got != c.want {
			t.Errorf("BlocksProposal(%d, tip %d) = %v, want %v", c.height, c.tip, got, c.want)
		}
	}
}

func TestReadFailureFailsClosed(t *testing.T) {
	s := newMemStore()
	s.failGet = true
	a := New()
	a.SetStore(s, "")
	if blocked, _, err := a.BlocksProposal(10, 9); !blocked || err == nil {
		t.Fatal("an unreadable store must block proposals")
	}
	if pending, err := a.PendingAt(10); !pending || err == nil {
		t.Fatal("an unreadable store must report the height as pending")
	}
}

// A Clear that fails after the block was stored must not stop the next height
// from being recorded: ClearStale drops the leftover record first.
func TestClearStaleAfterFailedClearLetsNextHeightBeRecorded(t *testing.T) {
	s := newMemStore()
	a := New()
	a.SetStore(s, "")
	if err := a.Save(rec(10, "0xaa")); err != nil {
		t.Fatal(err)
	}
	s.failPut = true
	if err := a.Clear(10, "0xaa"); err == nil {
		t.Fatal("the failed clear must be reported")
	}
	if err := a.ClearStale(10); err == nil {
		t.Fatal("the failed store delete must be reported")
	}
	if got, _ := a.Current(); got != nil {
		t.Fatalf("the stale record must be dropped from memory even when the store delete fails, got %+v", got)
	}
	s.failPut = false
	if err := a.Save(rec(11, "0xbb")); err != nil {
		t.Fatalf("height 11 must be recorded after the stale record is dropped: %v", err)
	}
	if blocked, _, _ := a.BlocksProposal(11, 10); !blocked {
		t.Fatal("height 11 must block proposals")
	}
	b := New()
	b.SetStore(s, "")
	if got, _ := b.Current(); got == nil || got.Height != 11 {
		t.Fatalf("height 11 must survive a restart, got %+v", got)
	}
}

// A record above the committed tip belongs to an uncommitted block: keep it,
// and keep refusing other heights.
func TestClearStaleKeepsUncommittedRecord(t *testing.T) {
	a := New()
	_ = a.Save(rec(10, "0xaa"))
	if err := a.ClearStale(9); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Current(); got == nil || got.Height != 10 {
		t.Fatalf("an uncommitted record must be kept, got %+v", got)
	}
	if err := a.Save(rec(11, "0xbb")); err == nil {
		t.Fatal("another height must still be refused while 10 is uncommitted")
	}
}

// A clear for another block at the same height keeps the record.
func TestClearOtherBlockIsNoop(t *testing.T) {
	a := New()
	_ = a.Save(rec(10, "0xaa"))
	_ = a.Clear(10, "0xbb")
	if got, _ := a.Current(); got == nil {
		t.Fatal("clearing a different block must keep the pending record")
	}
	if err := a.Clear(10, "0xAA"); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Current(); got != nil {
		t.Fatal("clearing the recorded block (any hex case) must remove it")
	}
}

// DropMatching removes the record only for a matching height and hash, and
// reports a mismatch with the current record.
func TestDropMatching(t *testing.T) {
	s := newMemStore()
	a := New()
	a.SetStore(s, "")
	if _, err := a.DropMatching(10, "0xaa"); !errors.Is(err, ErrNoPending) {
		t.Fatalf("want ErrNoPending, got %v", err)
	}
	_ = a.Save(rec(10, "0xaa"))
	if got, err := a.DropMatching(10, "0xbb"); !errors.Is(err, ErrRecordMismatch) || got == nil || got.BlockHash != "0xaa" {
		t.Fatalf("want ErrRecordMismatch with the current record, got %+v %v", got, err)
	}
	if got, err := a.DropMatching(10, "0xAA"); err != nil || got.Height != 10 {
		t.Fatalf("drop: %+v %v", got, err)
	}
	b := New()
	b.SetStore(s, "")
	if got, _ := b.Current(); got != nil {
		t.Fatal("a dropped record must not come back after a restart")
	}
}

// A stored record that cannot be decoded refuses proposals (fail closed) and
// can be removed only with DropUnreadable; a readable one is refused there.
func TestDropUnreadable(t *testing.T) {
	s := newMemStore()
	s.kv[DefaultKey] = []byte("{not json")
	a := New()
	a.SetStore(s, "")
	if _, err := a.Current(); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("want ErrUnreadable, got %v", err)
	}
	if blocked, _, _ := a.BlocksProposal(5, 4); !blocked {
		t.Fatal("an unreadable record must refuse proposals")
	}
	if _, err := a.DropMatching(5, "0xaa"); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("DropMatching cannot read it: want ErrUnreadable, got %v", err)
	}
	if err := a.DropUnreadable(); err != nil {
		t.Fatalf("DropUnreadable: %v", err)
	}
	if blocked, _, _ := a.BlocksProposal(5, 4); blocked {
		t.Fatal("proposals must be accepted after the unreadable record is dropped")
	}
	if len(s.kv[DefaultKey]) != 0 {
		t.Fatal("the store key must be empty")
	}
	if err := a.DropUnreadable(); !errors.Is(err, ErrNoPending) {
		t.Fatalf("want ErrNoPending, got %v", err)
	}

	b := New()
	b.SetStore(newMemStore(), "")
	_ = b.Save(rec(10, "0xaa"))
	if err := b.DropUnreadable(); !errors.Is(err, ErrReadable) {
		t.Fatalf("a readable record must be refused, got %v", err)
	}
	if got, _ := b.Current(); got == nil {
		t.Fatal("a refused DropUnreadable must keep the record")
	}
}
