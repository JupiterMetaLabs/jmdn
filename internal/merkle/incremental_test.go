package merkle

import (
	"context"
	"testing"

	merkletree "github.com/JupiterMetaLabs/JMDN_Merkletree/merkletree"
)

// leaf produces a distinct, deterministic leaf hash for a given seed.
func leaf(i int) merkletree.Hash32 {
	var h merkletree.Hash32
	h[0] = byte(i)
	h[1] = byte(i >> 8)
	h[31] = 0xAB
	return h
}

// countingScanner serves leaves from an in-memory chain and records how many
// leaves were read, so tests can assert the incremental path reads only the
// delta (tip + new blocks) rather than the whole chain.
type countingScanner struct {
	chain *[]merkletree.Hash32
	reads int
}

func (c *countingScanner) scan(ctx context.Context, start, end uint64) ([]merkletree.Hash32, error) {
	out := make([]merkletree.Hash32, 0, end-start+1)
	for h := start; h <= end; h++ {
		out = append(out, (*c.chain)[h])
	}
	c.reads += len(out)
	return out, nil
}

// fullRoot is the from-scratch oracle: the Merkle root over the entire leaf slice.
func fullRoot(t *testing.T, leaves []merkletree.Hash32) [32]byte {
	t.Helper()
	head := uint64(len(leaves)) - 1
	r, err := rootFromLeaves(head, leaves)
	if err != nil {
		t.Fatalf("rootFromLeaves: %v", err)
	}
	return r
}

// The incremental fold must produce the exact same root as a full rebuild after
// every append. This is the core correctness guarantee (drop-in for the sync
// monitor).
func TestFingerprinter_IncrementalMatchesFullAcrossAppends(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 4; i++ {
		chain = append(chain, leaf(i)) // heights 0..4, head 4
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(0) // disable periodic rebuild to isolate the fold

	for target := 4; target <= 40; target++ {
		if target > 4 {
			chain = append(chain, leaf(target))
		}
		res, err := fp.compute(context.Background(), headFn, sc.scan)
		if err != nil {
			t.Fatalf("compute at head %d: %v", target, err)
		}
		if want := fullRoot(t, chain); res.Root != want {
			t.Fatalf("head %d: incremental root %x != full-rebuild %x", target, res.Root, want)
		}
		if res.Head != uint64(target) {
			t.Fatalf("head %d: Result.Head = %d", target, res.Head)
		}
	}
}

// After the cold-start scan, an append must read only the tip + the new block —
// not the whole chain. This is the whole point of the change.
func TestFingerprinter_AppendReadsOnlyDelta(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 100; i++ {
		chain = append(chain, leaf(i)) // head 100
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(0)

	if _, err := fp.compute(context.Background(), headFn, sc.scan); err != nil {
		t.Fatal(err)
	}
	if sc.reads != 101 { // cold start read all 0..100
		t.Fatalf("cold start read %d leaves, want 101", sc.reads)
	}

	// append two blocks
	chain = append(chain, leaf(101), leaf(102))
	before := sc.reads
	if _, err := fp.compute(context.Background(), headFn, sc.scan); err != nil {
		t.Fatal(err)
	}
	// tip check (1) + new blocks 101..102 (2) = 3
	if delta := sc.reads - before; delta != 3 {
		t.Fatalf("append read %d leaves, want 3 (tip + 2 new)", delta)
	}
}

// A second Compute with no new blocks must return the cached root and read only
// the tip block (one leaf) to confirm nothing changed.
func TestFingerprinter_NoChangeReturnsCachedRoot(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 10; i++ {
		chain = append(chain, leaf(i))
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(0)

	r1, err := fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	before := sc.reads
	r2, err := fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Root != r2.Root {
		t.Fatalf("cached root changed: %x != %x", r1.Root, r2.Root)
	}
	if delta := sc.reads - before; delta != 1 {
		t.Fatalf("no-change compute read %d leaves, want 1 (tip only)", delta)
	}
}

// A reorg rewrites the reorged block and all its descendants (hashBlock chains
// via PrevHash+StateRoot). The tip check must catch it and rebuild to the
// correct root.
func TestFingerprinter_ReorgRebuildsToCorrectRoot(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 12; i++ {
		chain = append(chain, leaf(i)) // head 12
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(0)

	if _, err := fp.compute(context.Background(), headFn, sc.scan); err != nil {
		t.Fatal(err)
	}

	// Reorg at height 8: rewrite 8..12 (descendants change with the ancestor).
	for h := 8; h <= 12; h++ {
		chain[h] = leaf(h + 1000)
	}
	res, err := fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	if want := fullRoot(t, chain); res.Root != want {
		t.Fatalf("post-reorg root %x != full-rebuild %x", res.Root, want)
	}

	// A tip-only rewrite (single block at head) must also be caught.
	chain[len(chain)-1] = leaf(9999)
	res, err = fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	if want := fullRoot(t, chain); res.Root != want {
		t.Fatalf("post-tip-rewrite root %x != full-rebuild %x", res.Root, want)
	}
}

// With the periodic full rebuild enabled, every Compute must still return the
// correct root regardless of which path (append vs forced rebuild) it takes.
func TestFingerprinter_PeriodicRebuildStillCorrect(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 3; i++ {
		chain = append(chain, leaf(i))
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(3) // force a full rebuild every 3 computes

	for target := 3; target <= 25; target++ {
		if target > 3 {
			chain = append(chain, leaf(target))
		}
		res, err := fp.compute(context.Background(), headFn, sc.scan)
		if err != nil {
			t.Fatal(err)
		}
		if want := fullRoot(t, chain); res.Root != want {
			t.Fatalf("head %d (periodic): root %x != full %x", target, res.Root, want)
		}
	}
}

// Gaps (missing blocks → zero leaves) must fold identically to a full rebuild.
func TestFingerprinter_GapLeavesMatchFull(t *testing.T) {
	chain := make([]merkletree.Hash32, 0)
	for i := 0; i <= 6; i++ {
		if i == 3 || i == 5 {
			chain = append(chain, merkletree.Hash32{}) // gap
			continue
		}
		chain = append(chain, leaf(i))
	}
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return uint64(len(chain)) - 1 }
	fp := NewFingerprinter(0)

	res, err := fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	if want := fullRoot(t, chain); res.Root != want {
		t.Fatalf("gap chain: incremental %x != full %x", res.Root, want)
	}
}

// head == 0 must return an empty Result (matching BuildLocalMerkleRoot).
func TestFingerprinter_HeadZeroEmptyResult(t *testing.T) {
	chain := []merkletree.Hash32{leaf(0)} // only height 0 → head 0
	sc := &countingScanner{chain: &chain}
	headFn := func() uint64 { return 0 }
	fp := NewFingerprinter(0)

	res, err := fp.compute(context.Background(), headFn, sc.scan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != ([32]byte{}) || res.Head != 0 {
		t.Fatalf("head 0: got root %x head %d, want empty", res.Root, res.Head)
	}
}
