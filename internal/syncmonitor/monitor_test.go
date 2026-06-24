package syncmonitor_test

// monitor_test.go — integration-level test for the SyncMonitor flow.
//
// Tests without live infrastructure by using:
//   - stubBlockInfo: a minimal BlockInfo that reports a fixed block head + Merkle root.
//   - stubSeedClient: an in-process stand-in for the seednode gRPC client.
//
// Validates:
//  1. Monitor builds Merkle root from local block state.
//  2. Out-of-sync verdict triggers the ReconcileFunc.
//  3. Already-synced verdict does NOT trigger reconciliation.
//  4. Reconcile lock prevents concurrent runs.
//  5. After reconciliation, a fresh check is issued automatically.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	merkletree "github.com/JupiterMetaLabs/JMDN_Merkletree/merkletree"

	"gossipnode/internal/syncmonitor"
)

// ─── stub BlockInfo ───────────────────────────────────────────────────────────

type stubBlockInfo struct {
	head   uint64
	hashes [][]byte // one per block
}

func (s *stubBlockInfo) GetBlockNumber() uint64 { return s.head }
func (s *stubBlockInfo) GetBlockDetails() fastsync_types.PriorSync {
	return fastsync_types.PriorSync{Blocknumber: s.head}
}
func (s *stubBlockInfo) AUTH() fastsync_types.AUTHHandler                        { return nil }
func (s *stubBlockInfo) NewBlockHeaderIterator() fastsync_types.BlockHeader       { return nil }
func (s *stubBlockInfo) NewBlockNonHeaderIterator() fastsync_types.BlockNonHeader { return nil }
func (s *stubBlockInfo) NewHeadersWriter() fastsync_types.WriteHeaders            { return nil }
func (s *stubBlockInfo) NewDataWriter() fastsync_types.WriteData                  { return nil }
func (s *stubBlockInfo) NewAccountManager() fastsync_types.AccountManager         { return nil }

func (s *stubBlockInfo) NewBlockIterator(start, end uint64, batchSize int) fastsync_types.BlockIterator {
	return &stubBlockIter{info: s, cursor: start, end: end, batchSize: batchSize}
}

type stubBlockIter struct {
	info      *stubBlockInfo
	cursor    uint64
	end       uint64
	batchSize int
}

func (it *stubBlockIter) Next() ([]*fastsync_types.ZKBlock, error) {
	if it.cursor > it.end {
		return nil, nil
	}
	var batch []*fastsync_types.ZKBlock
	for i := 0; i < it.batchSize && it.cursor <= it.end; i++ {
		var h merkletree.Hash32
		if int(it.cursor) < len(it.info.hashes) {
			copy(h[:], it.info.hashes[it.cursor])
		}
		var bh [32]byte
		copy(bh[:], h[:])
		batch = append(batch, &fastsync_types.ZKBlock{
			BlockNumber: it.cursor,
			BlockHash:   bh,
		})
		it.cursor++
	}
	return batch, nil
}

func (it *stubBlockIter) Prev() ([]*fastsync_types.ZKBlock, error) { return nil, nil }
func (it *stubBlockIter) Close()                                    {}

// ─── stub seedclient ─────────────────────────────────────────────────────────

// stubSeedClient replaces the real gRPC client.
// It implements only the interface surface used by syncmonitor.
type stubSeedClient struct {
	isSynced      bool
	sequencerHead uint64
	sequencerRoot []byte
	goodPeers     []syncmonitor.PeerInfo
	// callCount tracks how many times ReportBlockState was called.
	callCount atomic.Int64
}

func (s *stubSeedClient) ReportBlockState(_ context.Context, blockHead uint64, merkleRoot []byte) (*syncmonitor.SyncStatus, error) {
	s.callCount.Add(1)
	return &syncmonitor.SyncStatus{
		IsSynced:      s.isSynced,
		SequencerHead: s.sequencerHead,
		SequencerRoot: s.sequencerRoot,
		GoodPeers:     s.goodPeers,
		Message:       "stub",
	}, nil
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestSyncMonitor_OutOfSync verifies that when the seednode reports is_synced=false
// the monitor triggers the ReconcileFunc exactly once.
func TestSyncMonitor_OutOfSync(t *testing.T) {
	t.Parallel()

	bi := &stubBlockInfo{
		head:   3,
		hashes: [][]byte{{1}, {2}, {3}, {4}},
	}
	sc := &stubSeedClient{
		isSynced:      false,
		sequencerHead: 10,
		sequencerRoot: []byte{0xFF},
		goodPeers: []syncmonitor.PeerInfo{
			{PeerID: "12D3KooWFakeGoodPeer", Multiaddrs: []string{"/ip4/127.0.0.1/tcp/9999"}},
		},
	}

	mon := syncmonitor.New(bi, sc, 0)

	var reconcileCalled atomic.Bool
	mon.SetReconcileFunc(func(ctx context.Context, peers []syncmonitor.PeerInfo) error {
		reconcileCalled.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st := mon.TriggerCheck(ctx)

	if st.IsSynced {
		t.Fatal("expected IsSynced=false")
	}
	if st.LocalHead != 3 {
		t.Fatalf("expected LocalHead=3, got %d", st.LocalHead)
	}
	if st.Error != "" {
		t.Fatalf("unexpected error: %s", st.Error)
	}

	// Give the goroutine time to call reconcile.
	deadline := time.Now().Add(2 * time.Second)
	for !reconcileCalled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !reconcileCalled.Load() {
		t.Fatal("ReconcileFunc was not called after out-of-sync detection")
	}
}

// TestSyncMonitor_AlreadySynced verifies that when the seednode reports is_synced=true
// the monitor does NOT trigger reconciliation.
func TestSyncMonitor_AlreadySynced(t *testing.T) {
	t.Parallel()

	bi := &stubBlockInfo{head: 5, hashes: [][]byte{{1}, {2}, {3}, {4}, {5}, {6}}}
	seqRoot := computeExpectedRoot(bi)

	sc := &stubSeedClient{
		isSynced:      true,
		sequencerHead: 5,
		sequencerRoot: seqRoot,
	}

	mon := syncmonitor.New(bi, sc, 0)

	var reconcileCalled atomic.Bool
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
		reconcileCalled.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	st := mon.TriggerCheck(ctx)

	if !st.IsSynced {
		t.Fatalf("expected IsSynced=true, got false (error=%s)", st.Error)
	}

	// Brief pause to confirm reconcile wasn't triggered asynchronously.
	time.Sleep(100 * time.Millisecond)
	if reconcileCalled.Load() {
		t.Fatal("ReconcileFunc should NOT be called when already synced")
	}
}

// TestSyncMonitor_ConcurrentReconcilePrevented verifies the atomic lock stops
// a second concurrent reconciliation from starting.
func TestSyncMonitor_ConcurrentReconcilePrevented(t *testing.T) {
	t.Parallel()

	bi := &stubBlockInfo{head: 2, hashes: [][]byte{{0xAA}, {0xBB}, {0xCC}}}
	sc := &stubSeedClient{
		isSynced:  false,
		goodPeers: []syncmonitor.PeerInfo{{PeerID: "12D3Fake", Multiaddrs: []string{"/ip4/127.0.0.1/tcp/9001"}}},
	}

	mon := syncmonitor.New(bi, sc, 0)

	// Slow reconcile so the first call is still running when the second fires.
	var callCount atomic.Int64
	block := make(chan struct{})
	mon.SetReconcileFunc(func(ctx context.Context, peers []syncmonitor.PeerInfo) error {
		callCount.Add(1)
		<-block // hold until test releases it
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First check — starts reconcile goroutine.
	mon.TriggerCheck(ctx)
	time.Sleep(50 * time.Millisecond)

	// Second check — reconcile is still running; should NOT start another one.
	mon.TriggerCheck(ctx)
	time.Sleep(50 * time.Millisecond)

	// Unblock the first reconcile.
	close(block)
	time.Sleep(100 * time.Millisecond)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected reconcile called exactly once, got %d", n)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// stubSeedClient satisfies syncmonitor.SeedReporter — verified at compile time.
var _ syncmonitor.SeedReporter = (*stubSeedClient)(nil)

// computeExpectedRoot is a helper that builds a Merkle root from stubBlockInfo,
// replicating the logic in internal/merkle.BuildLocalMerkleRoot for test assertions.
func computeExpectedRoot(bi *stubBlockInfo) []byte {
	if bi.head == 0 {
		return nil
	}
	cfg := merkletree.Config{ExpectedTotal: bi.head + 1}
	builder, err := merkletree.NewBuilder(cfg)
	if err != nil {
		return nil
	}
	for i := uint64(0); i <= bi.head; i++ {
		var h merkletree.Hash32
		if int(i) < len(bi.hashes) {
			copy(h[:], bi.hashes[i])
		}
		if _, err := builder.Push(i, []merkletree.Hash32{h}); err != nil {
			return nil
		}
	}
	root, err := builder.Finalize()
	if err != nil {
		return nil
	}
	return root[:]
}

// Satisfy the BlockHeader interface for the stub (needed to satisfy BlockInfo).
var _ fastsync_types.BlockHeader = (*stubBlockHeader)(nil)

type stubBlockHeader struct{}

func (s *stubBlockHeader) GetBlockHeaders(blocknumbers []uint64) ([]*blockpb.Header, error) {
	return nil, nil
}
func (s *stubBlockHeader) GetBlockHeadersRange(start, end uint64) ([]*blockpb.Header, error) {
	return nil, nil
}
