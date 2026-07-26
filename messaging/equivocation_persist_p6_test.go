package messaging

// Equivocation records survive a process restart. The in-memory seenHeights map
// is lost on restart; a durable EquivocationStore lets a node still reject a
// conflicting block at a height it first saw before the restart. These tests
// inject an in-memory fake store and simulate a restart by clearing seenHeights
// while keeping the store — the durable path is what must catch the conflict.

import (
	"context"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// fakeEquivStore is an in-memory EquivocationStore whose contents persist across
// a simulated restart (resetEquivocation only clears the in-memory seenHeights
// map, not this store).
type fakeEquivStore struct{ m map[uint64]string }

func newFakeEquivStore() *fakeEquivStore { return &fakeEquivStore{m: map[uint64]string{}} }

func (f *fakeEquivStore) FirstSeenHash(height uint64) (string, bool, error) {
	v, ok := f.m[height]
	return v, ok, nil
}

func (f *fakeEquivStore) RecordFirstSeen(height uint64, hashHex string) error {
	if _, ok := f.m[height]; !ok {
		f.m[height] = hashHex
	}
	return nil
}

// p6Block builds a block whose BlockHash is the canonical hash of its txs (body
// binding is on by default), mirroring the shared newBlock helper.
func p6Block(num uint64, txs ...config.Transaction) *config.ZKBlock {
	return &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		Transactions: txs,
	}
}

// TestP6_EquivocationSurvivesRestart covers the "node restart then same-height
// conflicting block" case. With the durable store wired, a different block at a
// height first seen before the restart is rejected.
func TestP6_EquivocationSurvivesRestart(t *testing.T) {
	ctx := context.Background()

	store := newFakeEquivStore()
	SetEquivocationStore(store)
	t.Cleanup(func() { SetEquivocationStore(nil) })
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	key2, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey2: %v", err)
	}

	// First validated block at height 50 — records its hash durably.
	b1 := p6Block(50, signedTx(t, key, 0))
	m1 := config.BlockMessage{Block: b1, Data: blockBoundCert(t, b1, "peerA", "peerB", "peerC")}
	if rej := validateRemoteBlock(ctx, m1); rej != nil {
		t.Fatalf("first block should pass, got %s", rej.reason)
	}
	if _, ok := store.m[50]; !ok {
		t.Fatalf("height 50 should have been recorded durably")
	}

	// Simulate a RESTART: the in-memory map is wiped, the durable store persists.
	resetEquivocation()

	// A DIFFERENT block at the same height 50 must now be rejected purely from
	// the durable record (in-memory has no knowledge of height 50 post-restart).
	b2 := p6Block(50, signedTx(t, key2, 0))
	if b2.BlockHash == b1.BlockHash {
		t.Fatal("test setup: b1 and b2 must differ")
	}
	m2 := config.BlockMessage{Block: b2, Data: blockBoundCert(t, b2, "peerA", "peerB", "peerC")}
	if rej := validateRemoteBlock(ctx, m2); rej == nil || rej.reason != "equivocation" {
		t.Fatalf("post-restart conflicting block should be rejected as equivocation, got %v", rej)
	}

	// The SAME block re-delivered after restart is NOT equivocation (same hash).
	resetEquivocation()
	if rej := validateRemoteBlock(ctx, m1); rej != nil {
		t.Fatalf("same block re-delivered post-restart should pass, got %s", rej.reason)
	}
}

// TestP6_WithoutStore_RestartLosesRecord documents WHY the durable store is
// required: with no store wired (in-memory only), a restart wipes the record
// and the conflicting block is no longer caught. This pins that gap so a
// regression that silently drops the store is visible.
func TestP6_WithoutStore_RestartLosesRecord(t *testing.T) {
	ctx := context.Background()

	SetEquivocationStore(nil) // explicit: in-memory only
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	key2, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey2: %v", err)
	}

	b1 := p6Block(60, signedTx(t, key, 0))
	m1 := config.BlockMessage{Block: b1, Data: blockBoundCert(t, b1, "peerA", "peerB", "peerC")}
	if rej := validateRemoteBlock(ctx, m1); rej != nil {
		t.Fatalf("first block should pass, got %s", rej.reason)
	}

	resetEquivocation() // simulate restart; no durable store to recover from

	b2 := p6Block(60, signedTx(t, key2, 0))
	m2 := config.BlockMessage{Block: b2, Data: blockBoundCert(t, b2, "peerA", "peerB", "peerC")}
	// Without a durable store the conflict is NOT caught — demonstrates the gap
	// the durable store closes. (If this ever rejects, the store is being set
	// globally and the test's premise changed.)
	if rej := validateRemoteBlock(ctx, m2); rej != nil && rej.reason == "equivocation" {
		t.Fatalf("without a store, restart should lose the record (in-memory only); got equivocation — store leaked in?")
	}
}
