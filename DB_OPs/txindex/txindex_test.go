package txindex

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gossipnode/config"
)

// ── test helpers ─────────────────────────────────────────────────────────────

// newTestDB opens a fresh SQLite-backed index in a temp directory and
// registers cleanup. Deliberately bypasses the package singleton (Init) so
// tests never touch global state — each test gets its own isolated *DB.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	idx, err := Open(filepath.Join(dir, "sub", "txindex.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func addr(hex string) *common.Address {
	a := common.HexToAddress(hex)
	return &a
}

// makeTx builds a minimal Transaction with a unique hash derived from seed.
func makeTx(from, to *common.Address, seed byte) config.Transaction {
	var h common.Hash
	h[31] = seed
	h[30] = seed / 2
	return config.Transaction{
		Hash: h,
		From: from,
		To:   to,
	}
}

func makeBlock(number uint64, txs ...config.Transaction) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber:  number,
		Transactions: txs,
	}
}

// countRows returns the number of rows in address_txns — used to assert
// dedup behaviour directly against the underlying table.
func countRows(t *testing.T, idx *DB) int {
	t.Helper()
	var n int
	err := idx.writeDB.QueryRow(`SELECT COUNT(*) FROM address_txns`).Scan(&n)
	require.NoError(t, err)
	return n
}

// ── Open/Close lifecycle ─────────────────────────────────────────────────────

func TestOpen_CreatesNestedDirAndSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "does", "not", "exist", "yet", "txindex.db")

	idx, err := Open(dbPath)
	require.NoError(t, err, "Open should create missing parent directories")
	defer idx.Close()

	// Schema must exist and be queryable on both pools.
	var name string
	require.NoError(t, idx.writeDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='address_txns'`,
	).Scan(&name))
	assert.Equal(t, "address_txns", name)

	require.NoError(t, idx.readDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='index_meta'`,
	).Scan(&name))
	assert.Equal(t, "index_meta", name)
}

func TestClose_ClosesBothPoolsAndIsSafeToCallTwice(t *testing.T) {
	idx := newTestDB(t)

	require.NoError(t, idx.Close())
	// database/sql documents Close as safe to call multiple times.
	assert.NoError(t, idx.Close())

	// Any further use of either pool should now fail instead of silently
	// operating on a closed handle.
	err := idx.writeDB.Ping()
	assert.Error(t, err, "writeDB should be unusable after Close")
}

// ── indexBlocks: writes, lowercasing, dedup, nil-safety ─────────────────────

func TestIndexBlocks_InsertsFromAndToLowercased(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()

	from := addr("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	to := addr("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	block := makeBlock(10, makeTx(from, to, 1))

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{block}))

	assert.Equal(t, 2, countRows(t, idx), "one row for `from`, one for `to`")

	n, err := idx.CountByAddress(ctx, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Mixed-case input must resolve to the same (lowercased) row.
	n, err = idx.CountByAddress(ctx, "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestIndexBlocks_NilBlockAndZeroTxAreNoops(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()

	// A nil entry in the slice must not panic and must not affect other blocks.
	from := addr("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	block := makeBlock(1, makeTx(from, nil, 1))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{nil, block, nil}))
	assert.Equal(t, 1, countRows(t, idx))

	// IndexBlock with zero transactions should be a complete no-op.
	require.NoError(t, idx.IndexBlock(ctx, makeBlock(2)))
	assert.Equal(t, 1, countRows(t, idx))

	// IndexBlock(nil) must not panic.
	require.NoError(t, idx.IndexBlock(ctx, nil))
}

func TestIndexBlocks_DuplicateInsertsAreIgnored(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()

	from := addr("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	block := makeBlock(5, makeTx(from, nil, 7))

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{block}))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{block})) // re-index same block
	assert.Equal(t, 1, countRows(t, idx), "INSERT OR IGNORE must prevent duplicate rows")
}

// ── last_indexed_block: monotonic under out-of-order writes ─────────────────
// This is the exact race fixed in round 2: IndexBlockAsync (live blocks) and
// buildRange (catchup) can commit out of order. The stored high-water mark
// must never move backwards regardless of commit order.

func TestSetMetaMonotonicMax_NeverDecreases(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()

	setAndGet := func(v uint64) uint64 {
		tx, err := idx.writeDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, setMetaMonotonicMax(tx, "last_indexed_block", v))
		require.NoError(t, tx.Commit())
		got, err := idx.lastIndexedBlock(ctx)
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, uint64(100), setAndGet(100))
	assert.Equal(t, uint64(100), setAndGet(50), "a lower value must not regress the stored max")
	assert.Equal(t, uint64(150), setAndGet(150), "a higher value must still advance the max")
	assert.Equal(t, uint64(150), setAndGet(150), "setting the same value is a no-op, not an error")
}

func TestIndexBlocks_LastIndexedBlockMonotonic_UnderOutOfOrderCommits(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	from := addr("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	// Simulate a live block (200) landing before a slower catchup batch (100)
	// commits — exactly the ordering that used to corrupt the watermark.
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(200, makeTx(from, nil, 1))}))
	last, err := idx.lastIndexedBlock(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(200), last)

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(100, makeTx(from, nil, 2))}))
	last, err = idx.lastIndexedBlock(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(200), last, "an older batch committing later must not move the watermark backwards")
}

func TestLastIndexedBlock_ZeroWhenNoMetaRow(t *testing.T) {
	idx := newTestDB(t)
	last, err := idx.lastIndexedBlock(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), last)
}

// ── pagination: determinism, tiebreak, clamping, cancellation ───────────────

// seedManyTxsSameBlock creates n transactions for the same address, all in
// the same block — the scenario that previously produced non-deterministic
// ordering (and therefore duplicate/skipped rows across pages) because the
// ORDER BY had no tiebreaker beyond block_number.
func seedManyTxsSameBlock(t *testing.T, idx *DB, address *common.Address, n int, blockNum uint64) {
	t.Helper()
	txs := make([]config.Transaction, n)
	for i := 0; i < n; i++ {
		txs[i] = makeTx(address, nil, byte(i+1))
	}
	require.NoError(t, idx.indexBlocks(context.Background(), []*config.ZKBlock{makeBlock(blockNum, txs...)}))
}

func TestGetTxRefsByOffset_NoDuplicatesOrGapsAcrossPagesWithSameBlockTies(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")

	const total = 37
	const pageSize = 10
	seedManyTxsSameBlock(t, idx, who, total, 42) // all rows share block_number=42

	seen := map[string]bool{}
	for offset := 0; offset < total+pageSize; offset += pageSize {
		refs, err := idx.GetTxRefsByOffset(ctx, who.Hex(), offset, pageSize)
		require.NoError(t, err)
		for _, r := range refs {
			assert.False(t, seen[r.TxHash], "tx_hash %s returned on more than one page", r.TxHash)
			seen[r.TxHash] = true
		}
	}
	assert.Len(t, seen, total, "every indexed tx must appear exactly once across all pages")
}

func TestGetTxRefsByOffset_StableOrderingAcrossRepeatedCalls(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0xDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")
	seedManyTxsSameBlock(t, idx, who, 15, 7)

	first, err := idx.GetTxRefsByOffset(ctx, who.Hex(), 0, 100)
	require.NoError(t, err)
	require.Len(t, first, 15)

	for i := 0; i < 5; i++ {
		again, err := idx.GetTxRefsByOffset(ctx, who.Hex(), 0, 100)
		require.NoError(t, err)
		require.Equal(t, len(first), len(again))
		for j := range first {
			assert.Equal(t, first[j].TxHash, again[j].TxHash, "ordering must be deterministic across repeated identical queries")
		}
	}
}

func TestGetTxRefsByOffset_NewestBlockFirst(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(1, makeTx(who, nil, 1))}))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(5, makeTx(who, nil, 2))}))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(3, makeTx(who, nil, 3))}))

	refs, err := idx.GetTxRefsByOffset(ctx, who.Hex(), 0, 10)
	require.NoError(t, err)
	require.Len(t, refs, 3)
	assert.Equal(t, []uint64{5, 3, 1}, []uint64{refs[0].BlockNumber, refs[1].BlockNumber, refs[2].BlockNumber})
}

func TestGetTxRefsByOffset_ClampsLimitAndOffset(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	seedManyTxsSameBlock(t, idx, who, 5, 1)

	// limit <= 0 falls back to defaultPageSize (not zero rows, not unbounded).
	refs, err := idx.GetTxRefsByOffset(ctx, who.Hex(), 0, 0)
	require.NoError(t, err)
	assert.Len(t, refs, 5)

	// limit above maxPageSize falls back to defaultPageSize, not maxPageSize
	// and not the raw oversized value.
	refs, err = idx.GetTxRefsByOffset(ctx, who.Hex(), 0, maxPageSize+1000)
	require.NoError(t, err)
	assert.Len(t, refs, 5)

	// negative offset must not error or panic — clamped to 0.
	refs, err = idx.GetTxRefsByOffset(ctx, who.Hex(), -50, 10)
	require.NoError(t, err)
	assert.Len(t, refs, 5)
}

func TestGetTxRefsByOffset_RespectsCancelledContext(t *testing.T) {
	idx := newTestDB(t)
	who := addr("0x1111111111111111111111111111111111111a")
	seedManyTxsSameBlock(t, idx, who, 3, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	_, err := idx.GetTxRefsByOffset(ctx, who.Hex(), 0, 10)
	assert.Error(t, err, "a caller-cancelled context must abort the query instead of running to completion")
}

func TestGetTxHashesByAddress_CursorPagination(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0x2222222222222222222222222222222222222b")

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(1, makeTx(who, nil, 1))}))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(2, makeTx(who, nil, 2))}))
	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(3, makeTx(who, nil, 3))}))

	firstPage, err := idx.GetTxHashesByAddress(ctx, who.Hex(), 0, 2)
	require.NoError(t, err)
	require.Len(t, firstPage, 2)
	assert.Equal(t, uint64(3), firstPage[0].BlockNumber)
	assert.Equal(t, uint64(2), firstPage[1].BlockNumber)

	// Page forward using the last row's block number as cursor.
	nextPage, err := idx.GetTxHashesByAddress(ctx, who.Hex(), firstPage[len(firstPage)-1].BlockNumber, 2)
	require.NoError(t, err)
	require.NotEmpty(t, nextPage)
	assert.Equal(t, uint64(2), nextPage[0].BlockNumber, "cursor is inclusive of the boundary block")
}

func TestCountByAddress_MatchesActualRowCount(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0x3333333333333333333333333333333333333c")
	seedManyTxsSameBlock(t, idx, who, 9, 1)

	n, err := idx.CountByAddress(ctx, who.Hex())
	require.NoError(t, err)
	assert.Equal(t, 9, n)

	other := addr("0x4444444444444444444444444444444444444d")
	n, err = idx.CountByAddress(ctx, other.Hex())
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// ── sanity: writer/reader pool separation actually points at the same file ──

func TestWriteThenReadIsVisibleAcrossPools(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()
	who := addr("0x5555555555555555555555555555555555555e")

	require.NoError(t, idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(1, makeTx(who, nil, 1))}))

	// CountByAddress/GetTxRefsByOffset read via idx.readDB; indexBlocks wrote
	// via idx.writeDB. A committed write must be visible to the reader pool
	// immediately (this is exactly what WAL mode guarantees).
	n, err := idx.CountByAddress(ctx, who.Hex())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// ── concurrency smoke test: concurrent writers don't corrupt or deadlock ────
// Run with -race to catch data races; the single-writer-connection design
// (writeDB.SetMaxOpenConns(1) + busy_timeout) should serialize these safely.

func TestIndexBlocks_ConcurrentWritesDoNotDeadlockOrCorrupt(t *testing.T) {
	idx := newTestDB(t)
	who := addr("0x6666666666666666666666666666666666666f")

	const goroutines = 8
	const blocksEach = 10

	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := 0; i < blocksEach; i++ {
				blockNum := uint64(g*blocksEach + i)
				tx := makeTx(who, nil, byte(blockNum%251+1))
				// vary the hash further so goroutines never collide on PK
				tx.Hash[29] = byte(g)
				tx.Hash[28] = byte(i)
				if err := idx.indexBlocks(ctx, []*config.ZKBlock{makeBlock(blockNum, tx)}); err != nil {
					errCh <- fmt.Errorf("goroutine %d block %d: %w", g, blockNum, err)
					return
				}
			}
			errCh <- nil
		}(g)
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}

	n, err := idx.CountByAddress(context.Background(), who.Hex())
	require.NoError(t, err)
	assert.Equal(t, goroutines*blocksEach, n)

	last, err := idx.lastIndexedBlock(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(goroutines*blocksEach-1), last, "watermark must land on the true max regardless of write order")
}

// ── singleton: Shutdown() vs IndexBlockAsync() close race ───────────────────
// This is the one test in this file that touches package-level singleton
// state (globalIdx/asyncQueue/shuttingDown/shutdownCtx). It deliberately
// bypasses Init() — which kicks off EnsureReady against a live ImmuDB pool,
// unavailable in this test environment — and instead wires up just enough
// singleton state by hand to exercise the actual race under test. Cleans up
// afterward so it stays isolated from every other test in this file.

func TestShutdown_ClosingAsyncQueueDoesNotRaceWithIndexBlockAsync(t *testing.T) {
	idx := newTestDB(t)

	globalMu.Lock()
	globalIdx = idx
	globalMu.Unlock()
	shutdownCtx, shutdownCancel = context.WithCancel(context.Background())
	asyncQueue = make(chan *config.ZKBlock, 16)
	shuttingDown.Store(false)
	go asyncIndexWorker()

	t.Cleanup(func() {
		globalMu.Lock()
		globalIdx = nil
		globalMu.Unlock()
		// Deliberately NOT touching asyncQueue here: it's already closed (by
		// Shutdown() below) and the worker goroutine we started above may
		// still be in the middle of draining/exiting when cleanup runs.
		// Reassigning the package-level asyncQueue variable from this
		// goroutine while that one might still reference it is exactly the
		// unsynchronized-write bug Shutdown() itself was just fixed for —
		// leaving it as a closed-but-non-nil channel is harmless since
		// nothing else in the test binary touches the singleton.
		shuttingDown.Store(false)
		asyncCloseOnce = sync.Once{}
	})

	who := addr("0x7777777777777777777777777777777777777a")
	var wg sync.WaitGroup

	// Race a bounded burst of concurrent sends against Shutdown() — enough to
	// exercise the check+send/close ordering without an unbounded busy loop
	// (which, on a full queue, logs a line per dropped block and can flood
	// test output for no added coverage). Before the asyncSendMu fix, a
	// sender that read shuttingDown==false a moment before Shutdown() set it
	// could still be executing `asyncQueue <- block` when Shutdown() closed
	// the channel — a send-on-closed-channel panic, not just a hang. Under
	// `go test -race`, that panic (or a detected race on asyncQueue) is what
	// this test is designed to surface.
	const senders = 8
	const sendsPerSender = 25
	for g := 0; g < senders; g++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			for i := 0; i < sendsPerSender; i++ {
				IndexBlockAsync(makeBlock(1, makeTx(who, nil, seed)))
			}
		}(byte(g))
	}

	// Shut down immediately, while senders are still in flight, rather than
	// waiting for them to finish — that's the actual race being tested.
	assert.NotPanics(t, func() {
		assert.NoError(t, Shutdown())
	})
	wg.Wait()

	// asyncIndexWorker must have actually exited (channel closed, range loop
	// drained) rather than being left blocked on `range asyncQueue` forever —
	// the original bug report. A second Shutdown() call must stay a safe
	// no-op, not hang/panic.
	assert.NotPanics(t, func() {
		assert.NoError(t, Shutdown())
	})
}

// Compile-time guard: catches accidental signature drift on the sql.DB fields
// (e.g. someone re-merging idx.db back in) without needing reflection.
var _ = func() bool {
	var idx DB
	var _ *sql.DB = idx.writeDB
	var _ *sql.DB = idx.readDB
	return true
}()

// ── CountTransactions (explorer stats source) ────────────────────────────────

// TestCountTransactions_DistinctNotRows pins the DISTINCT semantics: the table
// stores one row per (address, tx) pair, so a plain transfer produces TWO rows
// (sender + recipient) for ONE transaction. COUNT(*) would report 2; the
// explorer's total-transactions stat must report 1.
func TestCountTransactions_DistinctNotRows(t *testing.T) {
	idx := newTestDB(t)
	ctx := context.Background()

	a := addr("0x1111111111111111111111111111111111111111")
	b := addr("0x2222222222222222222222222222222222222222")

	// tx1: a→b (2 rows, 1 tx). tx2: a→a self-send (1 row after PK dedup, 1 tx).
	require.NoError(t, idx.IndexBlock(ctx, makeBlock(1, makeTx(a, b, 1), makeTx(a, a, 2))))

	require.Equal(t, 3, countRows(t, idx), "row count sanity: 2 (transfer) + 1 (self-send)")

	n, err := idx.CountTransactions(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), n, "distinct tx count must not double-count multi-address txs")

	// Re-indexing the same block (replay) must not change the count.
	require.NoError(t, idx.IndexBlock(ctx, makeBlock(1, makeTx(a, b, 1), makeTx(a, a, 2))))
	n, err = idx.CountTransactions(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), n, "replay must be idempotent")
}
