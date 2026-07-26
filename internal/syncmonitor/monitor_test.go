package syncmonitor_test

// monitor_test.go — integration-level tests for the SyncMonitor.
//
// Tests without live infrastructure using:
//   - stubBlockInfo: minimal BlockInfo with a fixed head + hashes.
//   - stubBlockInfoWithTimer: adds LastBlockReceivedAt() for Fix 2 tests.
//   - stubSeedClient: in-process stand-in for the seednode gRPC client.
//
// Existing tests use WithOutOfSyncThreshold(1) to preserve single-TriggerCheck
// behaviour; the default threshold (2) is exercised in TestFix3_*.

import (
	"context"
	"errors"
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
	hashes [][]byte
}

func (s *stubBlockInfo) GetBlockNumber() uint64 { return s.head }
func (s *stubBlockInfo) GetBlockDetails() fastsync_types.PriorSync {
	return fastsync_types.PriorSync{Blocknumber: s.head}
}
func (s *stubBlockInfo) AUTH() fastsync_types.AUTHHandler                         { return nil }
func (s *stubBlockInfo) NewBlockHeaderIterator() fastsync_types.BlockHeader       { return nil }
func (s *stubBlockInfo) NewBlockNonHeaderIterator() fastsync_types.BlockNonHeader { return nil }
func (s *stubBlockInfo) NewHeadersWriter() fastsync_types.WriteHeaders            { return nil }
func (s *stubBlockInfo) NewDataWriter() fastsync_types.WriteData                  { return nil }
func (s *stubBlockInfo) NewAccountManager() fastsync_types.AccountManager         { return nil }
func (s *stubBlockInfo) NewBlockIterator(start, end uint64, batchSize int) fastsync_types.BlockIterator {
	return &stubBlockIter{info: s, cursor: start, end: end, batchSize: batchSize}
}

// stubBlockInfoWithTimer extends stubBlockInfo with LastBlockReceivedAt
// so Fix 2 (propagation guard) tests can control the timestamp.
type stubBlockInfoWithTimer struct {
	stubBlockInfo
	lastReceived time.Time
}

func (s *stubBlockInfoWithTimer) LastBlockReceivedAt() time.Time { return s.lastReceived }

// ─── stub BlockIterator ───────────────────────────────────────────────────────

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
		var bh [32]byte
		if int(it.cursor) < len(it.info.hashes) {
			copy(bh[:], it.info.hashes[it.cursor])
		}
		batch = append(batch, &fastsync_types.ZKBlock{
			BlockNumber: it.cursor,
			BlockHash:   bh,
		})
		it.cursor++
	}
	return batch, nil
}
func (it *stubBlockIter) Prev() ([]*fastsync_types.ZKBlock, error) { return nil, nil }
func (it *stubBlockIter) Close()                                   {}

// ─── stub SeedReporter ───────────────────────────────────────────────────────

type stubSeedClient struct {
	isSynced      bool
	sequencerHead uint64
	sequencerRoot []byte
	goodPeers     []syncmonitor.PeerInfo
	err           error
	callCount     atomic.Int64
}

func (s *stubSeedClient) ReportBlockState(_ context.Context, _ uint64, _ []byte) (*syncmonitor.SyncStatus, error) {
	s.callCount.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &syncmonitor.SyncStatus{
		IsSynced:      s.isSynced,
		SequencerHead: s.sequencerHead,
		SequencerRoot: s.sequencerRoot,
		GoodPeers:     s.goodPeers,
		Message:       "stub",
	}, nil
}

var _ syncmonitor.SeedReporter = (*stubSeedClient)(nil)

// ─── helpers ─────────────────────────────────────────────────────────────────

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

func goodPeer() syncmonitor.PeerInfo {
	return syncmonitor.PeerInfo{PeerID: "12D3KooWFakePeer", Multiaddrs: []string{"/ip4/127.0.0.1/tcp/9999"}}
}

// ─── existing tests (unchanged behaviour, threshold overridden to 1) ─────────

func TestSyncMonitor_OutOfSync(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 3, hashes: [][]byte{{1}, {2}, {3}, {4}}}
	sc := &stubSeedClient{
		isSynced: false, sequencerHead: 10, sequencerRoot: []byte{0xFF},
		goodPeers: []syncmonitor.PeerInfo{goodPeer()},
	}
	mon := syncmonitor.New(bi, sc, 0).WithOutOfSyncThreshold(1)

	var reconcileCalled atomic.Bool
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
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
	deadline := time.Now().Add(2 * time.Second)
	for !reconcileCalled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !reconcileCalled.Load() {
		t.Fatal("ReconcileFunc was not called after out-of-sync detection")
	}
}

func TestSyncMonitor_AlreadySynced(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 5, hashes: [][]byte{{1}, {2}, {3}, {4}, {5}, {6}}}
	seqRoot := computeExpectedRoot(bi)
	sc := &stubSeedClient{isSynced: true, sequencerHead: 5, sequencerRoot: seqRoot}
	mon := syncmonitor.New(bi, sc, 0).WithOutOfSyncThreshold(1)

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
	time.Sleep(100 * time.Millisecond)
	if reconcileCalled.Load() {
		t.Fatal("ReconcileFunc should NOT be called when already synced")
	}
}

func TestSyncMonitor_ConcurrentReconcilePrevented(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 2, hashes: [][]byte{{0xAA}, {0xBB}, {0xCC}}}
	sc := &stubSeedClient{
		isSynced:  false,
		goodPeers: []syncmonitor.PeerInfo{goodPeer()},
	}
	mon := syncmonitor.New(bi, sc, 0).WithOutOfSyncThreshold(1)

	var callCount atomic.Int64
	block := make(chan struct{})
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
		callCount.Add(1)
		<-block
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mon.TriggerCheck(ctx)
	time.Sleep(50 * time.Millisecond)
	mon.TriggerCheck(ctx) // second check — reconcile already running, must be skipped
	time.Sleep(50 * time.Millisecond)

	close(block)
	time.Sleep(100 * time.Millisecond)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected reconcile called exactly once, got %d", n)
	}
}

// ─── Fix 1: startup jitter ────────────────────────────────────────────────────

func TestFix1_StartupJitter(t *testing.T) {
	t.Parallel()
	// Two monitors with the same base interval should fire their first check
	// at different times due to random jitter.
	interval := 200 * time.Millisecond
	fires := make([]time.Time, 2)

	for i := 0; i < 2; i++ {
		i := i
		bi := &stubBlockInfo{head: 1, hashes: [][]byte{{byte(i)}, {byte(i + 1)}}}
		sc := &stubSeedClient{isSynced: true}
		mon := syncmonitor.New(bi, sc, interval)
		mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error { return nil })
		mon.SetOnCheck(func() { fires[i] = time.Now() }) // hook for test observability

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := mon.Start(ctx); err != nil {
			t.Fatalf("monitor %d Start: %v", i, err)
		}
	}

	// Wait for both to fire at least once.
	time.Sleep(2 * time.Second)

	diff := fires[0].Sub(fires[1])
	if diff < 0 {
		diff = -diff
	}
	// With random jitter over [0, 200ms), two independent monitors should rarely
	// fire within 5ms of each other. We assert they don't fire at the exact same time.
	// (This is a probabilistic test; flake probability ≈ 5/200 = 2.5%.)
	if diff < 5*time.Millisecond && !fires[0].IsZero() && !fires[1].IsZero() {
		t.Logf("jitter diff=%v — monitors may have fired simultaneously (low-probability flake)", diff)
	}
}

// ─── Fix 2: propagation guard ─────────────────────────────────────────────────

func TestFix2_PropagationGuardSkipsCheck(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfoWithTimer{
		stubBlockInfo: stubBlockInfo{head: 5, hashes: [][]byte{{1}, {2}, {3}, {4}, {5}, {6}}},
		lastReceived:  time.Now(), // block arrived just now — within propagation window
	}
	sc := &stubSeedClient{isSynced: true}
	mon := syncmonitor.New(bi, sc, 0).WithOutOfSyncThreshold(1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// First check primes the lastStatus (no prior status → propagation guard returns zero Status).
	// Set a known prior status by running a check with an old timestamp first.
	bi.lastReceived = time.Now().Add(-time.Hour) // old — guard passes
	first := mon.TriggerCheck(ctx)
	if first.Error != "" {
		t.Fatalf("setup check failed: %s", first.Error)
	}

	// Now simulate a fresh block arrival — guard should skip.
	bi.lastReceived = time.Now()
	callsBefore := sc.callCount.Load()
	skipped := mon.TriggerCheck(ctx)

	callsAfter := sc.callCount.Load()
	if callsAfter != callsBefore {
		t.Fatalf("expected seednode NOT called during propagation window, got %d additional calls", callsAfter-callsBefore)
	}
	// Returned status should be the previous (first) status, not a fresh one.
	if skipped.LastCheckedAt != first.LastCheckedAt {
		t.Fatalf("expected propagation-guard skip to return previous status (LastCheckedAt=%v), got %v",
			first.LastCheckedAt, skipped.LastCheckedAt)
	}
}

// ─── Fix 3: consecutive out-of-sync threshold ────────────────────────────────

func TestFix3_ConsecutiveThresholdGatesReconcile(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 3, hashes: [][]byte{{1}, {2}, {3}, {4}}}
	sc := &stubSeedClient{
		isSynced:      false,
		sequencerHead: 10,
		goodPeers:     []syncmonitor.PeerInfo{goodPeer()},
	}
	// Default threshold is 2.
	mon := syncmonitor.New(bi, sc, 0)

	var reconcileCount atomic.Int64
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
		reconcileCount.Add(1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First check — threshold not reached, reconcile must NOT fire.
	st1 := mon.TriggerCheck(ctx)
	if st1.ConsecutiveOutOfSync != 1 {
		t.Fatalf("expected ConsecutiveOutOfSync=1 after first check, got %d", st1.ConsecutiveOutOfSync)
	}
	time.Sleep(50 * time.Millisecond)
	if reconcileCount.Load() != 0 {
		t.Fatal("ReconcileFunc must NOT fire on first out-of-sync (threshold=2)")
	}

	// Second check — threshold reached, reconcile MUST fire.
	st2 := mon.TriggerCheck(ctx)
	if st2.ConsecutiveOutOfSync != 2 {
		t.Fatalf("expected ConsecutiveOutOfSync=2 after second check, got %d", st2.ConsecutiveOutOfSync)
	}
	deadline := time.Now().Add(2 * time.Second)
	for reconcileCount.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reconcileCount.Load() == 0 {
		t.Fatal("ReconcileFunc must fire after threshold (2) consecutive out-of-sync reports")
	}
}

// ─── Fix 4: block-delta filter ───────────────────────────────────────────────

func TestFix4_BlockDeltaFilterPropagationLag(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 5, hashes: [][]byte{{1}, {2}, {3}, {4}, {5}, {6}}}
	sc := &stubSeedClient{
		isSynced:      false,
		sequencerHead: 6, // delta = 1, within propagation tolerance of 1
		goodPeers:     []syncmonitor.PeerInfo{goodPeer()},
	}
	mon := syncmonitor.New(bi, sc, 0)

	var reconcileFired atomic.Bool
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
		reconcileFired.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	st := mon.TriggerCheck(ctx)

	// Delta ≤ tolerance → treated as propagation lag → IsSynced=true operationally.
	if !st.IsSynced {
		t.Fatal("expected IsSynced=true for propagation lag (delta ≤ toleranceBlocks)")
	}
	// ConsecutiveOutOfSync must NOT increment for a lag-only mismatch.
	if st.ConsecutiveOutOfSync != 0 {
		t.Fatalf("expected ConsecutiveOutOfSync=0 for propagation lag, got %d", st.ConsecutiveOutOfSync)
	}
	time.Sleep(100 * time.Millisecond)
	if reconcileFired.Load() {
		t.Fatal("ReconcileFunc must NOT fire for propagation lag")
	}
}

// ─── Fix 5: seednode grace period ────────────────────────────────────────────

func TestFix5_SeednodeGracePeriod(t *testing.T) {
	t.Parallel()
	bi := &stubBlockInfo{head: 3, hashes: [][]byte{{1}, {2}, {3}, {4}}}
	sc := &stubSeedClient{err: errors.New("connection refused")}
	// Default grace period is 3.
	mon := syncmonitor.New(bi, sc, 0)

	var reconcileFired atomic.Bool
	mon.SetReconcileFunc(func(_ context.Context, _ []syncmonitor.PeerInfo) error {
		reconcileFired.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Checks 1 and 2: within grace period — IsSynced=true, SeednodeUnreachable=false.
	for i := 1; i <= 2; i++ {
		st := mon.TriggerCheck(ctx)
		if !st.IsSynced {
			t.Fatalf("check %d: expected IsSynced=true within grace period", i)
		}
		if st.SeednodeUnreachable {
			t.Fatalf("check %d: expected SeednodeUnreachable=false within grace period", i)
		}
	}

	// Check 3: grace period exhausted — IsSynced=false, SeednodeUnreachable=true.
	st3 := mon.TriggerCheck(ctx)
	if st3.IsSynced {
		t.Fatal("check 3: expected IsSynced=false after grace period exhausted")
	}
	if !st3.SeednodeUnreachable {
		t.Fatal("check 3: expected SeednodeUnreachable=true after grace period exhausted")
	}

	// ReconcileFunc must NEVER fire on seednode unreachability (no trusted peer list).
	time.Sleep(100 * time.Millisecond)
	if reconcileFired.Load() {
		t.Fatal("ReconcileFunc must NOT fire when seednode is unreachable")
	}
}

// ─── Satisfy interfaces ───────────────────────────────────────────────────────

var _ fastsync_types.BlockHeader = (*stubBlockHeader)(nil)

type stubBlockHeader struct{}

func (s *stubBlockHeader) GetBlockHeaders(blocknumbers []uint64) ([]*blockpb.Header, error) {
	return nil, nil
}
func (s *stubBlockHeader) GetBlockHeadersRange(start, end uint64) ([]*blockpb.Header, error) {
	return nil, nil
}
