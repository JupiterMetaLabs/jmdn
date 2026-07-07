// Package txindex maintains a SQLite-backed address → transaction index.
// It serves as a lightweight lookup layer so RPC calls like eth_getTransactionsByAddress
// never need to scan ImmuDB. The index stores only (address, block_number, tx_hash) —
// full transaction data is fetched from ImmuDB by hash after the index lookup.
//
// Lifecycle:
//  1. Call Open() once at node startup to open/create the SQLite file.
//  2. Call EnsureReady() to detect gaps and run incremental catchup from ImmuDB.
//  3. Call IndexBlock() on every newly committed block to keep the index live.
//  4. Call GetTxHashesByAddress() from the RPC handler (paginated, max 50/page).
package txindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

const (
	migrationBatchSize = 500
	defaultPageSize    = 50
	maxPageSize        = 100

	// queryTimeout bounds every SQLite call made on the RPC/HTTP read path and
	// the write path, so a stuck disk or lock contention can't hang a caller
	// (or a goroutine) forever.
	queryTimeout = 5 * time.Second

	// sqliteBusyTimeoutMS tells the driver to retry internally (instead of
	// immediately returning SQLITE_BUSY) when a writer is momentarily holding
	// the single write lock. Needed because indexBlocks (async block commits),
	// RebuildRange/RebuildIndex (CLI) and read queries can all be in flight at
	// the same time.
	sqliteBusyTimeoutMS = 5000

	// asyncQueueSize bounds how many committed blocks can be buffered for
	// background indexing before IndexBlockAsync starts dropping them. This
	// caps memory/goroutine growth under load instead of spawning one
	// goroutine per block forever.
	asyncQueueSize = 2048

	// maxReadConns bounds the read pool. WAL allows concurrent readers while
	// a writer holds the write lock, so this is what actually gives us
	// concurrent RPC/HTTP reads during a long catchup/rebuild instead of
	// queuing every reader behind the single writer connection.
	maxReadConns = 8
)

// DB is the SQLite-backed address → transaction index.
// Reads and writes go through separate *sql.DB pools against the same file:
// writeDB is capped at one connection (go-sqlite3 does not serialize writers
// across pooled connections, so a single connection is the safe way to avoid
// SQLITE_BUSY), while readDB allows several concurrent connections so page
// reads are never queued behind a long-running catchup/rebuild transaction —
// WAL mode is specifically what makes that safe.
type DB struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// TxRef is a lightweight reference returned by GetTxHashesByAddress.
type TxRef struct {
	BlockNumber uint64
	TxHash      string
}

// Open opens (or creates) the SQLite index at dbPath.
// Always call EnsureReady() after Open() on node boot.
func Open(dbPath string) (*DB, error) {
	if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("txindex: create dir %s: %w", dir, err)
		}
	}

	// Pragmas are passed via the DSN (not a one-off db.Exec after Open) because
	// a pool with more than one connection (readDB below) opens new
	// connections lazily under load — a PRAGMA executed once against whichever
	// connection happened to serve that first Exec would NOT apply to
	// connections opened later. DSN params are applied by the driver to every
	// connection it opens, so this is the only way to make busy_timeout/WAL
	// reliable across a multi-connection pool.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=%d&_foreign_keys=on",
		dbPath, sqliteBusyTimeoutMS)

	writeDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("txindex: open writer %s: %w", dbPath, err)
	}
	// go-sqlite3 does not serialize writers across multiple pooled
	// connections the way a real client/server DB does, so the write path is
	// capped at exactly one connection — combined with busy_timeout this
	// avoids "database is locked" errors under concurrent write attempts
	// (IndexBlockAsync worker + RebuildRange/RebuildIndex CLI).
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	writeDB.SetConnMaxLifetime(0)

	readDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("txindex: open reader %s: %w", dbPath, err)
	}
	// Multiple read connections are safe (and desirable) under WAL: readers
	// never block on the writer, so RPC/HTTP pagination stays fast even
	// while a catchup/rebuild is writing in the background.
	readDB.SetMaxOpenConns(maxReadConns)
	readDB.SetMaxIdleConns(maxReadConns)

	if err := writeDB.Ping(); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("txindex: ping writer %s: %w", dbPath, err)
	}
	if err := readDB.Ping(); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("txindex: ping reader %s: %w", dbPath, err)
	}

	if err := createSchema(writeDB); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("txindex: create schema: %w", err)
	}

	return &DB{writeDB: writeDB, readDB: readDB}, nil
}

// EnsureReady is the startup availability check.
// It compares the last indexed block against ImmuDB's latest block.
// If behind (or empty), it runs incremental catchup automatically.
// Safe to call on every boot — no-op if already current.
func (idx *DB) EnsureReady(ctx context.Context) error {
	lastIndexed, err := idx.lastIndexedBlock(ctx)
	if err != nil {
		return fmt.Errorf("txindex: read last indexed block: %w", err)
	}

	latestBlock, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("txindex: get latest block from ThebeDB: %w", err)
	}

	if latestBlock == 0 {
		log.Printf("[txindex] No blocks in ThebeDB yet — nothing to index")
		return nil
	}

	if lastIndexed >= latestBlock {
		log.Printf("[txindex] Index is current at block %d", lastIndexed)
		return nil
	}

	gap := latestBlock - lastIndexed
	log.Printf("[txindex] Index behind: indexed=%d latest=%d gap=%d blocks — starting catchup",
		lastIndexed, latestBlock, gap)

	from := lastIndexed + 1
	if lastIndexed == 0 {
		from = 0 // first-time migration starts at genesis
	}

	if err := idx.buildRange(ctx, from, latestBlock); err != nil {
		return fmt.Errorf("txindex: catchup failed: %w", err)
	}

	log.Printf("[txindex] Catchup complete — index now at block %d", latestBlock)
	return nil
}

// IndexBlock indexes a single newly committed block.
// Call this from the block commit path immediately after writing to ImmuDB.
// It is fast (single SQLite transaction) and must not block the commit path.
// ctx should be a long-lived parent (e.g. the package shutdown context for
// the async worker) — a bounded per-call timeout is derived from it inside
// indexBlocks, so a single slow write can't eat another caller's budget.
func (idx *DB) IndexBlock(ctx context.Context, block *config.ZKBlock) error {
	if block == nil || len(block.Transactions) == 0 {
		return nil
	}
	return idx.indexBlocks(ctx, []*config.ZKBlock{block})
}

// CountByAddress returns the total number of transactions indexed for an address.
// Fast: backed by the idx_address_block index.
// ctx should be the caller's request context (e.g. the HTTP/RPC request) so
// a disconnected client aborts the query instead of it running to completion
// unread; queryTimeout still caps the worst case even with context.Background().
func (idx *DB) CountByAddress(ctx context.Context, address string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var n int
	err := idx.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM address_txns WHERE address = ?`, strings.ToLower(address),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("txindex: count address %s: %w", address, err)
	}
	return n, nil
}

// GetTxRefsByOffset returns paginated tx references for an address, newest first,
// using SQL OFFSET. Suitable for page-number–based UIs.
// Ordered by (block_number DESC, tx_hash DESC) — the tx_hash tiebreaker keeps
// page boundaries stable when an address has multiple transactions in the
// same block, which block_number alone cannot guarantee across separate
// OFFSET/LIMIT queries.
func (idx *DB) GetTxRefsByOffset(ctx context.Context, address string, offset, limit int) ([]TxRef, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}
	if offset < 0 {
		offset = 0
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := idx.readDB.QueryContext(ctx, `
		SELECT block_number, tx_hash
		FROM   address_txns
		WHERE  address = ?
		ORDER  BY block_number DESC, tx_hash DESC
		LIMIT  ? OFFSET ?
	`, strings.ToLower(address), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("txindex: offset query address %s: %w", address, err)
	}
	defer rows.Close()

	var results []TxRef
	for rows.Next() {
		var ref TxRef
		if err := rows.Scan(&ref.BlockNumber, &ref.TxHash); err != nil {
			return nil, fmt.Errorf("txindex: scan row: %w", err)
		}
		results = append(results, ref)
	}
	return results, rows.Err()
}

// GetTxHashesByAddress returns paginated tx references for an address, newest first.
// cursor = 0 starts from the newest; pass the BlockNumber of the last result to page forward.
// NOTE: a single uint64 block-number cursor cannot uniquely place a row when
// an address has several transactions in the same block — callers doing true
// cursor pagination should prefer GetTxRefsByOffset, or this should be
// extended to a compound (block_number, tx_hash) cursor before it is wired
// into a live handler.
func (idx *DB) GetTxHashesByAddress(ctx context.Context, address string, cursor uint64, limit int) ([]TxRef, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var (
		rows *sql.Rows
		err  error
	)

	addr := strings.ToLower(address)
	if cursor == 0 {
		rows, err = idx.readDB.QueryContext(ctx, `
			SELECT block_number, tx_hash
			FROM   address_txns
			WHERE  address = ?
			ORDER  BY block_number DESC, tx_hash DESC
			LIMIT  ?
		`, addr, limit)
	} else {
		rows, err = idx.readDB.QueryContext(ctx, `
			SELECT block_number, tx_hash
			FROM   address_txns
			WHERE  address = ? AND block_number <= ?
			ORDER  BY block_number DESC, tx_hash DESC
			LIMIT  ?
		`, addr, cursor, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("txindex: query address %s: %w", address, err)
	}
	defer rows.Close()

	var results []TxRef
	for rows.Next() {
		var ref TxRef
		if err := rows.Scan(&ref.BlockNumber, &ref.TxHash); err != nil {
			return nil, fmt.Errorf("txindex: scan row: %w", err)
		}
		results = append(results, ref)
	}
	return results, rows.Err()
}

// Close closes both underlying SQLite connection pools.
func (idx *DB) Close() error {
	writeErr := idx.writeDB.Close()
	readErr := idx.readDB.Close()
	if writeErr != nil {
		return writeErr
	}
	return readErr
}

// ── schema ───────────────────────────────────────────────────────────────────

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS address_txns (
			address      TEXT    NOT NULL,
			block_number INTEGER NOT NULL,
			tx_hash      TEXT    NOT NULL,
			PRIMARY KEY (address, tx_hash)
		);

		-- Fast descending lookup by address.
		CREATE INDEX IF NOT EXISTS idx_address_block
			ON address_txns(address, block_number DESC);

		-- Tracks the highest block fully indexed.
		-- Single row: key='last_indexed_block', value='<uint64>'.
		CREATE TABLE IF NOT EXISTS index_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	return err
}

// ── catchup ──────────────────────────────────────────────────────────────────

// buildRange iterates ImmuDB blocks [from, to] in batches of migrationBatchSize
// and writes index entries. Uses NewBlockIterator so it reuses the existing
// connection-pool logic and never holds more than one connection at a time.
func (idx *DB) buildRange(ctx context.Context, from, to uint64) error {
	iter := DB_OPs.NewBlockIterator(nil, from, to, migrationBatchSize)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		blocks, err := iter.Next()
		if err != nil {
			return fmt.Errorf("txindex: block iterator: %w", err)
		}
		if len(blocks) == 0 {
			break
		}

		if err := idx.indexBlocks(ctx, blocks); err != nil {
			return err
		}

		last := blocks[len(blocks)-1]
		log.Printf("[txindex] Indexed up to block %d / %d", last.BlockNumber, to)

		// Brief pause between batches — avoid saturating ImmuDB connection pool.
		time.Sleep(10 * time.Millisecond)
	}

	return nil
}

// ── write ────────────────────────────────────────────────────────────────────

// indexBlocks writes address→tx entries for a batch of blocks in one SQLite transaction.
// ctx is the caller's parent context (buildRange's catchup ctx, or the
// package shutdown context for the async worker) — writes get a longer
// per-call budget than reads (a batch can touch several hundred rows), but
// they still must not be able to outlive the parent (e.g. node shutdown).
func (idx *DB) indexBlocks(ctx context.Context, blocks []*config.ZKBlock) error {
	if len(blocks) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout*4)
	defer cancel()

	tx, err := idx.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("txindex: begin: %w", err)
	}
	defer func() {
		// Rollback after a successful Commit() is a guaranteed no-op that
		// returns sql.ErrTxDone — expected, not logged. Anything else (a
		// dropped connection, a driver-level error) means the write lock or
		// connection may not have been released cleanly, which is worth
		// knowing about rather than silently swallowing.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			log.Printf("[txindex] indexBlocks: rollback failed: %v", rbErr)
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO address_txns (address, block_number, tx_hash)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("txindex: prepare insert: %w", err)
	}
	defer stmt.Close()

	var lastBlock uint64
	for _, blk := range blocks {
		if blk == nil {
			continue
		}
		for _, t := range blk.Transactions {
			txHash := t.Hash.Hex()

			if t.From != nil {
				if _, err := stmt.ExecContext(ctx, strings.ToLower(t.From.Hex()), blk.BlockNumber, txHash); err != nil {
					return fmt.Errorf("txindex: insert from block %d: %w", blk.BlockNumber, err)
				}
			}
			if t.To != nil {
				if _, err := stmt.ExecContext(ctx, strings.ToLower(t.To.Hex()), blk.BlockNumber, txHash); err != nil {
					return fmt.Errorf("txindex: insert to block %d: %w", blk.BlockNumber, err)
				}
			}
		}
		if blk.BlockNumber > lastBlock {
			lastBlock = blk.BlockNumber
		}
	}

	if err := setMetaMonotonicMax(tx, "last_indexed_block", lastBlock); err != nil {
		return fmt.Errorf("txindex: update meta: %w", err)
	}

	return tx.Commit()
}

// ── meta helpers ─────────────────────────────────────────────────────────────

func (idx *DB) lastIndexedBlock(ctx context.Context) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var val string
	err := idx.readDB.QueryRowContext(ctx,
		`SELECT value FROM index_meta WHERE key = 'last_indexed_block'`,
	).Scan(&val)

	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var n uint64
	if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
		return 0, fmt.Errorf("txindex: parse last_indexed_block %q: %w", val, err)
	}
	return n, nil
}

// setMetaMonotonicMax upserts a numeric metadata value only if it is greater
// than what's already stored (numeric comparison, not text comparison).
// last_indexed_block MUST use this: IndexBlockAsync (live blocks) and
// buildRange (historical catchup) both call indexBlocks concurrently, and
// without a monotonic guard a catchup batch that commits AFTER a newer live
// block would silently move last_indexed_block BACKWARDS — corrupting the
// next EnsureReady() gap calculation.
func setMetaMonotonicMax(tx *sql.Tx, key string, value uint64) error {
	_, err := tx.Exec(`
		INSERT INTO index_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = CASE
			WHEN CAST(excluded.value AS INTEGER) > CAST(index_meta.value AS INTEGER)
			THEN excluded.value
			ELSE index_meta.value
		END
	`, key, fmt.Sprintf("%d", value))
	return err
}

// ── singleton ────────────────────────────────────────────────────────────────
// A process-level singleton so callers (broadcast.go, RPC handlers) can reach
// the index without threading *DB through every call site.

var (
	globalMu   sync.RWMutex
	globalIdx  *DB
	asyncQueue chan *config.ZKBlock
	asyncStart sync.Once // guards starting the background worker exactly once

	// ready flips true once the FIRST gap-catchup pass has completed
	// successfully. Query functions gate on this so callers get an honest
	// "still syncing" error instead of silently-incomplete results while a
	// genesis (or post-loss) rebuild is still running in the background.
	ready atomic.Bool

	// shuttingDown is checked by IndexBlockAsync so it stops accepting new
	// work as soon as Shutdown() is called, instead of racing to enqueue onto
	// a worker that's on its way out.
	shuttingDown atomic.Bool

	// asyncSendMu makes "check shuttingDown, then send" in IndexBlockAsync
	// mutually exclusive with closing asyncQueue in Shutdown(). Just setting
	// shuttingDown before closing is NOT sufficient on its own: a goroutine
	// that read shuttingDown==false a moment earlier can still be blocked on
	// `asyncQueue <- block` when the channel gets closed, which panics
	// (send on closed channel), not just hangs. Senders take RLock for the
	// duration of their check+send; Shutdown takes the write Lock before
	// closing, which can only succeed once every in-flight send has finished,
	// so the close can never race a send.
	asyncSendMu sync.RWMutex

	// asyncCloseOnce ensures asyncQueue is closed exactly once no matter how
	// many times Shutdown() is called. Deliberately NOT implemented by
	// setting the asyncQueue variable itself to nil after closing: that would
	// be a second, unsynchronized write to a package-level variable that
	// asyncIndexWorker's `range asyncQueue` also reads — asyncQueue is
	// written exactly once, when Init() creates it, and never reassigned
	// again, so there is nothing for the race detector (or a real
	// interleaving on a weakly-ordered platform) to catch.
	asyncCloseOnce sync.Once

	// shutdownCtx/shutdownCancel form the root of every background operation
	// this package starts on its own initiative (the async worker's writes,
	// the initial catchup goroutine). Deriving from context.Background() ONCE
	// here — at package-lifecycle scope, set from the ctx passed to Init — is
	// the correct place for it; scattering context.Background() calls inside
	// individual query/write methods (the previous approach) severs the
	// cancellation chain and makes graceful shutdown impossible. Defaults to
	// a no-op cancel so calling Shutdown() before Init() is a safe no-op.
	shutdownCtx    context.Context    = context.Background()
	shutdownCancel context.CancelFunc = func() {}
)

// getIdx returns the singleton under a read lock. Safe for concurrent use.
func getIdx() *DB {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalIdx
}

// IsReady reports whether the index has completed at least one full gap
// catchup and is safe to treat as authoritative for reads.
func IsReady() bool {
	return ready.Load()
}

// Init opens the SQLite index and starts the background worker synchronously
// (fast — just opening the DB file), then runs the potentially-long initial
// gap catchup in a goroutine so it never blocks the caller.
//
// This matters because main.go calls Init() before starting the facade/RPC
// server, consensus, and gossip — a synchronous catchup here would stall the
// ENTIRE node on every restart where the index is behind (first deploy, or
// after any index loss), not just tx-by-address lookups. Query functions
// return a clear error until IsReady() is true.
//
// A failed Open() (e.g. disk/permission issue) does NOT permanently wedge the
// package: callers may call Init again and it will retry from scratch as
// long as the singleton has not already been set.
//
// Once a call successfully sets globalIdx, Init is a permanent one-shot: any
// later call (even with a different dbPath) returns nil immediately without
// touching shutdownCtx/shutdownCancel/asyncStart again — the early
// `if globalIdx != nil` check below runs before shutdownCtx is ever
// (re)assigned, so the running async worker is never left bound to a stale
// context. This also means Init cannot be used to re-point the singleton at
// a different file mid-process (e.g. between test cases) — tests that need a
// fresh instance should exercise Open()/*DB directly instead of the
// singleton, which is what txindex_test.go does.
func Init(ctx context.Context, dbPath string) error {
	globalMu.Lock()
	if globalIdx != nil {
		globalMu.Unlock()
		return nil // already initialised
	}
	globalMu.Unlock()

	idx, err := Open(dbPath)
	if err != nil {
		return fmt.Errorf("txindex.Init: %w", err)
	}

	globalMu.Lock()
	if globalIdx != nil {
		// Lost a race with a concurrent Init — keep the winner, discard ours.
		globalMu.Unlock()
		idx.Close()
		return nil
	}
	globalIdx = idx
	globalMu.Unlock()

	// Every background operation this package starts on its own (async
	// worker writes, the initial catchup below) derives from this ctx rather
	// than a fresh context.Background() per call, so Shutdown() can actually
	// cancel work in flight instead of it running until the process dies.
	shutdownCtx, shutdownCancel = context.WithCancel(ctx)

	// Start the async worker BEFORE kicking off catchup below. If the queue
	// doesn't exist yet when a live block is committed mid-catchup,
	// IndexBlockAsync has nothing to enqueue to and drops the block silently
	// (no future catchup would know to look for it, since it's newer than
	// the range currently being scanned).
	asyncStart.Do(func() {
		asyncQueue = make(chan *config.ZKBlock, asyncQueueSize)
		go asyncIndexWorker()
	})

	go func() {
		if err := idx.EnsureReady(shutdownCtx); err != nil {
			log.Printf("[txindex] ALERT: initial catchup failed: %v — index is NOT ready; "+
				"address-by-tx lookups will return errors until this is retried (CLI `rebuildindex`) "+
				"or the next FastsyncV2 catchup runs EnsureReady again", err)
			return
		}
		ready.Store(true)
		log.Printf("[txindex] initial catchup complete — index is ready")
	}()

	return nil
}

// asyncIndexWorker drains asyncQueue serially. A single dedicated goroutine
// (rather than one goroutine per block) bounds memory/CPU under load and
// naturally serializes writes against the single SQLite writer connection.
// Each block's write derives its own bounded timeout from shutdownCtx (see
// indexBlocks), so cancelling shutdownCtx during Shutdown() aborts whatever
// write is in flight instead of it running to completion.
func asyncIndexWorker() {
	for block := range asyncQueue {
		idx := getIdx()
		if idx == nil || block == nil {
			continue
		}
		if err := idx.IndexBlock(shutdownCtx, block); err != nil {
			log.Printf("[txindex] IndexBlock block %d: %v", block.BlockNumber, err)
		}
	}
}

// IndexBlockAsync enqueues a committed block for background indexing so it
// never delays the block commit path. If the queue is full (index falling
// behind block production, or SQLite stalled), the block is dropped and
// logged loudly rather than spawning unbounded goroutines — RebuildRange/
// RebuildIndex or the next EnsureReady() catchup will fill the gap.
func IndexBlockAsync(block *config.ZKBlock) {
	if block == nil || getIdx() == nil {
		return
	}

	// Hold the send side of asyncSendMu for the whole check+send so Shutdown()
	// can't close asyncQueue out from under us mid-send (see asyncSendMu doc).
	asyncSendMu.RLock()
	defer asyncSendMu.RUnlock()

	if shuttingDown.Load() || asyncQueue == nil {
		return
	}
	select {
	case asyncQueue <- block:
	default:
		log.Printf("[txindex] ALERT: async index queue full (cap=%d) — dropped block %d, will be picked up by next gap catchup",
			asyncQueueSize, block.BlockNumber)
	}
}

// Shutdown stops the index from accepting new async work, cancels any
// in-flight background operation started by this package (initial catchup,
// or a queued async write), closes asyncQueue so asyncIndexWorker can exit
// instead of blocking on it forever, and closes both SQLite connection pools.
// Call this once during graceful node shutdown. Safe to call even if Init
// was never called (no-op), and safe to call more than once.
func Shutdown() error {
	shuttingDown.Store(true)
	shutdownCancel() // cancels the initial-catchup goroutine and unblocks any in-flight write

	// Taking the write lock waits for any IndexBlockAsync call currently
	// inside its check+send critical section to finish first, so by the time
	// we close the channel no goroutine can still be sending on it.
	// asyncCloseOnce (not a nil-check on asyncQueue itself) is what makes a
	// second Shutdown() call safe — see its doc comment above.
	asyncSendMu.Lock()
	asyncCloseOnce.Do(func() {
		if asyncQueue != nil {
			close(asyncQueue)
		}
	})
	asyncSendMu.Unlock()

	idx := getIdx()
	if idx == nil {
		return nil
	}
	return idx.Close()
}

// EnsureReady re-runs the startup gap check on the singleton index.
// Safe to call at any time (e.g. after fastsync completes) — it is a no-op if
// the index is already current. Returns silently if the singleton is not initialised.
// On success, marks the index ready (covers the case where the background
// pass kicked off by Init failed or is still in flight, and this call —
// e.g. from FastsyncV2 after a catchup sync — is the one that actually
// closes the gap).
func EnsureReady(ctx context.Context) error {
	idx := getIdx()
	if idx == nil {
		return nil
	}
	if err := idx.EnsureReady(ctx); err != nil {
		return err
	}
	ready.Store(true)
	return nil
}

// RebuildRange re-indexes a specific block range [from, to] regardless of what
// last_indexed_block says. Use this to fill known gaps — e.g. after PoTS writes
// blocks that were missing when the original catchup ran.
// INSERT OR IGNORE means existing rows are safe; no duplicates are created.
func RebuildRange(ctx context.Context, from, to uint64) error {
	idx := getIdx()
	if idx == nil {
		return fmt.Errorf("txindex not initialised")
	}
	log.Printf("[txindex] RebuildRange [%d..%d]", from, to)
	if err := idx.buildRange(ctx, from, to); err != nil {
		return fmt.Errorf("txindex: RebuildRange [%d..%d]: %w", from, to, err)
	}
	log.Printf("[txindex] RebuildRange [%d..%d] complete", from, to)
	return nil
}

// RebuildIndex wipes the entire index and re-indexes from genesis.
// Use when gap detection finds the high-water mark is ahead of actual coverage.
// Blocks the caller until complete — run in a goroutine if needed.
// The index is marked not-ready for the duration: between the truncate and
// the completed re-catchup, reads would otherwise silently return an empty
// result set that looks like "address has no history" instead of "rebuild in
// progress".
func RebuildIndex(ctx context.Context) error {
	idx := getIdx()
	if idx == nil {
		return fmt.Errorf("txindex not initialised")
	}
	log.Printf("[txindex] RebuildIndex: wiping and re-indexing from genesis")
	ready.Store(false)

	// Truncate both tables inside a single transaction.
	tx, err := idx.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("txindex: RebuildIndex begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM address_txns; DELETE FROM index_meta;`); err != nil {
		// The truncate error (err) is what's returned to the caller either
		// way; a rollback failure here is secondary but still worth a log —
		// this is a destructive op (wiping the whole index), so a lock or
		// connection left in a bad state afterward should not go unnoticed.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			log.Printf("[txindex] RebuildIndex: rollback after truncate failure: %v", rbErr)
		}
		return fmt.Errorf("txindex: RebuildIndex truncate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("txindex: RebuildIndex commit: %w", err)
	}

	// Re-index everything from ImmuDB.
	if err := idx.EnsureReady(ctx); err != nil {
		return err
	}
	ready.Store(true)
	return nil
}

// errNotReady is returned by every read entry point while the index has not
// yet completed its first full gap catchup (genesis migration, or a
// RebuildIndex in progress). Callers should surface this as "temporarily
// unavailable" rather than treating an empty result as "no history".
var errNotReady = fmt.Errorf("txindex: initial sync in progress, try again shortly")

// QueryByAddress is the RPC-facing lookup. Returns paginated tx refs.
// ctx should be the caller's request context so a client disconnect or
// caller-side deadline actually cancels the underlying SQLite query.
func QueryByAddress(ctx context.Context, address string, cursor uint64, limit int) ([]TxRef, error) {
	idx := getIdx()
	if idx == nil {
		return nil, fmt.Errorf("txindex not initialised")
	}
	if !ready.Load() {
		return nil, errNotReady
	}
	return idx.GetTxHashesByAddress(ctx, address, cursor, limit)
}

// QueryByAddressOffset returns a page of tx refs using SQL OFFSET (page-number UIs).
// ctx should be the caller's request context (see QueryByAddress).
func QueryByAddressOffset(ctx context.Context, address string, offset, limit int) ([]TxRef, error) {
	idx := getIdx()
	if idx == nil {
		return nil, fmt.Errorf("txindex not initialised")
	}
	if !ready.Load() {
		return nil, errNotReady
	}
	return idx.GetTxRefsByOffset(ctx, address, offset, limit)
}

// CountByAddress returns the total indexed tx count for an address.
// ctx should be the caller's request context (see QueryByAddress).
func CountByAddress(ctx context.Context, address string) (int, error) {
	idx := getIdx()
	if idx == nil {
		return 0, fmt.Errorf("txindex not initialised")
	}
	if !ready.Load() {
		return 0, errNotReady
	}
	return idx.CountByAddress(ctx, address)
}

// Status reports operational state for CLI/health-check use: whether the
// index has completed its first full gap catchup, and the highest block
// number it has fully indexed so far (useful to watch progress during a
// long genesis migration or rebuild). Deliberately does not gate on `ready`
// itself — this is the one query meant to work WHILE the index is catching up.
func Status(ctx context.Context) (isReady bool, lastIndexedBlock uint64, err error) {
	idx := getIdx()
	if idx == nil {
		return false, 0, fmt.Errorf("txindex not initialised")
	}
	last, err := idx.lastIndexedBlock(ctx)
	if err != nil {
		return ready.Load(), 0, err
	}
	return ready.Load(), last, nil
}
