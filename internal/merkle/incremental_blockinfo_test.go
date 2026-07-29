package merkle

import (
	"context"
	"testing"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

// F1 regression guard: the incremental Fingerprinter must produce a byte-identical
// root to the deployed BuildLocalMerkleRoot for the same chain, INCLUDING when the
// block iterator yields ragged batches. BuildLocalMerkleRoot pushes to the builder
// per iterator batch (ragged), while the incremental path reads leaves then pushes
// fixed-size chunks — so equal roots here prove Push batch boundaries do not affect
// the root (confirmed independently against JMDN_Merkletree's chunk-by-BlockMerge
// logic). This uses a real fastsync_types.BlockInfo, not the leafScanFn seam.

// ---- fake BlockInfo / BlockIterator (only GetBlockNumber + NewBlockIterator used) ----

type fakeBlockInfo struct {
	blocks   []*fastsync_types.ZKBlock // index == height; nil = gap
	schedule []int                     // ragged batch sizes for Next(); empty => use batchsize
}

func (f *fakeBlockInfo) GetBlockNumber() uint64 { return uint64(len(f.blocks)) - 1 }

func (f *fakeBlockInfo) NewBlockIterator(start, end uint64, batchsize int) fastsync_types.BlockIterator {
	if batchsize <= 0 {
		batchsize = 1
	}
	return &fakeIter{blocks: f.blocks, pos: start, end: end, schedule: f.schedule, defBatch: batchsize}
}

// Unused by the fingerprint path — panic-stubs keep the interface satisfied.
func (f *fakeBlockInfo) AUTH() fastsync_types.AUTHHandler          { panic("unused in test") }
func (f *fakeBlockInfo) GetBlockDetails() fastsync_types.PriorSync { panic("unused in test") }
func (f *fakeBlockInfo) NewBlockHeaderIterator() fastsync_types.BlockHeader {
	panic("unused in test")
}
func (f *fakeBlockInfo) NewBlockNonHeaderIterator() fastsync_types.BlockNonHeader {
	panic("unused in test")
}
func (f *fakeBlockInfo) NewHeadersWriter() fastsync_types.WriteHeaders    { panic("unused in test") }
func (f *fakeBlockInfo) NewDataWriter() fastsync_types.WriteData          { panic("unused in test") }
func (f *fakeBlockInfo) NewAccountManager() fastsync_types.AccountManager { panic("unused in test") }

type fakeIter struct {
	blocks   []*fastsync_types.ZKBlock
	pos      uint64
	end      uint64
	schedule []int
	schedIdx int
	defBatch int
}

func (it *fakeIter) Next() ([]*fastsync_types.ZKBlock, error) {
	if it.pos > it.end {
		return nil, nil
	}
	n := it.defBatch
	if len(it.schedule) > 0 {
		if it.schedIdx < len(it.schedule) {
			n = it.schedule[it.schedIdx]
			it.schedIdx++
		}
	}
	if n <= 0 {
		n = 1
	}
	out := make([]*fastsync_types.ZKBlock, 0, n)
	for i := 0; i < n && it.pos <= it.end; i++ {
		out = append(out, it.blocks[it.pos])
		it.pos++
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (it *fakeIter) Prev() ([]*fastsync_types.ZKBlock, error) { panic("unused in test") }
func (it *fakeIter) Close()                                   {}

// ---- the test ----

func TestFingerprinter_MatchesBuildLocalMerkleRoot_RaggedIterator(t *testing.T) {
	ctx := context.Background()

	// Chain 0..1200 with one gap at height 700, distinct blocks via BlockNumber.
	var blocks []*fastsync_types.ZKBlock
	for i := 0; i <= 1200; i++ {
		if i == 700 {
			blocks = append(blocks, nil) // gap → zero leaf
			continue
		}
		blocks = append(blocks, &fastsync_types.ZKBlock{BlockNumber: uint64(i)})
	}

	// Deliberately ragged iterator batches; the final oversized entry is clamped
	// to the range end. This makes BuildLocalMerkleRoot's per-batch Push boundaries
	// differ from the incremental path's fixed chunks.
	bi := &fakeBlockInfo{blocks: blocks, schedule: []int{450, 1000, 3, 1, 999, 7, 5000}}
	fp := NewFingerprinter(0) // isolate the fold; no forced rebuild

	assertEqual := func(tag string) {
		t.Helper()
		full, err := BuildLocalMerkleRoot(ctx, bi)
		if err != nil {
			t.Fatalf("%s: BuildLocalMerkleRoot: %v", tag, err)
		}
		inc, err := fp.Compute(ctx, bi)
		if err != nil {
			t.Fatalf("%s: Compute: %v", tag, err)
		}
		if inc.Root != full.Root {
			t.Fatalf("%s: incremental root %x != full-path root %x", tag, inc.Root, full.Root)
		}
		if inc.Head != full.Head {
			t.Fatalf("%s: head %d != %d", tag, inc.Head, full.Head)
		}
	}

	// Cold start: incremental (fixed-chunk push) vs full path (ragged push).
	assertEqual("cold")

	// Incremental appends must keep matching the full path at each new head,
	// still with the ragged iterator in play.
	for i := 1201; i <= 1260; i++ {
		// Append to bi.blocks (not the local slice) so the fake actually grows —
		// GetBlockNumber/NewBlockIterator read bi.blocks.
		bi.blocks = append(bi.blocks, &fastsync_types.ZKBlock{BlockNumber: uint64(i)})
		assertEqual("append")
	}
}
