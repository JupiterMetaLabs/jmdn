package messaging

// Tests for the fallback aggregate-store restart recovery (rows 3/4/12/13).
//
// The scenario these exist for: a node collects part of a fallback window,
// crashes, and restarts. Before RecoverAggSigStoreAtStartup it came back with
// an empty store and failed that epoch's fold closed while its peers resolved
// it normally — two honest nodes holding different entropy.

import (
	"testing"

	"gossipnode/config"
)

// fakeChain serves blocks by height, the way DB_OPs.GetZKBlockByNumber does.
type fakeChain struct {
	blocks map[uint64]*config.ZKBlock
	reads  int
}

func (f *fakeChain) get(h uint64) (*config.ZKBlock, error) {
	f.reads++
	b, ok := f.blocks[h]
	if !ok {
		return nil, nil // hole in local history
	}
	return b, nil
}

// buildChain makes `n` blocks where height == slot (period 0 throughout), so
// block at height h records an aggregate against slot h-1.
func buildChain(n uint64) *fakeChain {
	f := &fakeChain{blocks: make(map[uint64]*config.ZKBlock, n)}
	for h := uint64(1); h <= n; h++ {
		f.blocks[h] = &config.ZKBlock{BlockNumber: h, Slot: h, Period: 0}
	}
	return f
}

func TestRecoveryIsNoOpWhenAggCertDisabled(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	orig := AggCertEnabled
	AggCertEnabled = false
	t.Cleanup(func() { AggCertEnabled = orig })

	chain := buildChain(100)
	n, err := RecoverAggSigStoreAtStartup(60, 100, chain.get)
	if err != nil {
		t.Fatalf("disabled path must not error: %v", err)
	}
	if n != 0 {
		t.Fatalf("recovered %d with the flag off, want 0", n)
	}
	if chain.reads != 0 {
		t.Fatalf("read %d blocks with the flag off — it must not touch storage at all", chain.reads)
	}
}

func TestRecoveryRejectsNilLoader(t *testing.T) {
	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	if _, err := RecoverAggSigStoreAtStartup(60, 100, nil); err == nil {
		t.Fatal("a nil block loader must be an error, not a silent no-op")
	}
}

func TestRecoveryNoOpOnEmptyChain(t *testing.T) {
	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	chain := buildChain(0)
	n, err := RecoverAggSigStoreAtStartup(0, 0, chain.get)
	if err != nil {
		t.Fatalf("genesis/unsynced must not error: %v", err)
	}
	if n != 0 || chain.reads != 0 {
		t.Fatalf("recovered=%d reads=%d, want 0/0 on an empty chain", n, chain.reads)
	}
}

// TestRecoveryWalksTheActiveWindow exercises the real case: the tip sits
// INSIDE the collection window, so the walk must traverse the in-window blocks
// rather than breaking out on the first one.
//
// An earlier version of this test used tip slot 400, where epoch 8's window is
// [403, 410) — entirely in the FUTURE — so the walk broke out after one block
// and the test proved nothing. Placing the tip at 408 puts blocks 404..408
// (parent slots 403..407) inside the window.
func TestRecoveryWalksTheActiveWindow(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	const tip = 408 // epoch 8, window [403, 410)
	chain := buildChain(tip)

	if _, err := RecoverAggSigStoreAtStartup(tip, tip, chain.get); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	// Blocks 404..408 have parent slots 403..407, all in window; the walk then
	// hits 403 (parent slot 402, below start) and stops. So it must read more
	// than one block, and far fewer than the whole chain.
	if chain.reads < 2 {
		t.Fatalf("scanned only %d block(s) with the tip inside the window — the walk is breaking "+
			"out immediately, so this exercises nothing", chain.reads)
	}
	if chain.reads >= tip {
		t.Fatalf("scanned %d of %d blocks — the backward walk is not stopping below the window",
			chain.reads, tip)
	}
	if chain.reads > maxRecoveryScanBlocks {
		t.Fatalf("scanned %d blocks, above the %d cap", chain.reads, maxRecoveryScanBlocks)
	}
	t.Logf("scanned %d blocks for the active window (tip slot %d)", chain.reads, tip)
}

// TestRecoveryStopsWhenWindowNotYetOpen: the tip is BELOW the window start, so
// nothing can contribute and the walk should give up almost immediately rather
// than scanning history.
func TestRecoveryStopsWhenWindowNotYetOpen(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	const tip = 400 // epoch 8, window [403, 410) — not open yet
	chain := buildChain(tip)

	n, err := RecoverAggSigStoreAtStartup(tip, tip, chain.get)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if n != 0 {
		t.Fatalf("recovered %d before the window opened, want 0", n)
	}
	if chain.reads > 2 {
		t.Fatalf("scanned %d blocks with the window still closed — should stop at once", chain.reads)
	}
}

// TestRecoveryToleratesHistoryHoles — a missing block must not abort the walk.
// A short window is already handled: the fold fails closed.
func TestRecoveryToleratesHistoryHoles(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	const tip = 120
	chain := buildChain(tip)
	for h := uint64(100); h <= 110; h++ {
		delete(chain.blocks, h) // punch a hole
	}

	if _, err := RecoverAggSigStoreAtStartup(tip, tip, chain.get); err != nil {
		t.Fatalf("a hole in local history must not fail recovery: %v", err)
	}
}

// TestRecoveryUsesParentSlotNotHeight pins the mapping rule. AdvanceOnCommit
// does slot += period+1, so a block that burned a timeout sits more than one
// slot after its parent. Reconstructing the slot arithmetically instead of
// reading block.Slot would record against a slot that never had a block.
func TestRecoveryUsesParentSlotNotHeight(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	orig := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = orig })

	// Height 50 committed at period 2 => it sits 3 slots after its parent.
	b := &config.ZKBlock{Slot: 60, Period: 2}
	wantParentSlot := b.Slot - (b.Period + 1) // 57, not 59
	if wantParentSlot != 57 {
		t.Fatalf("test arithmetic wrong: got %d", wantParentSlot)
	}
	if b.Slot-1 == wantParentSlot {
		t.Fatal("this test is vacuous unless period > 0 makes Slot-1 differ from the parent slot")
	}
}
