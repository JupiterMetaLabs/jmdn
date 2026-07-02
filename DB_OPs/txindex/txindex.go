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
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

const (
	migrationBatchSize = 500
	defaultPageSize    = 50
	maxPageSize        = 100
)

// DB is the SQLite-backed address → transaction index.
type DB struct {
	db *sql.DB
}

// TxRef is a lightweight reference returned by GetTxHashesByAddress.
type TxRef struct {
	BlockNumber uint64
	TxHash      string
}

// Open opens (or creates) the SQLite index at dbPath.
// Always call EnsureReady() after Open() on node boot.
func Open(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("txindex: open %s: %w", dbPath, err)
	}

	// WAL mode: concurrent reads don't block the single writer.
	// NORMAL sync: safe enough (we can rebuild from ImmuDB on crash).
	if _, err := db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous  = NORMAL;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("txindex: set pragmas: %w", err)
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("txindex: create schema: %w", err)
	}

	return &DB{db: db}, nil
}

// EnsureReady is the startup availability check.
// It compares the last indexed block against ImmuDB's latest block.
// If behind (or empty), it runs incremental catchup automatically.
// Safe to call on every boot — no-op if already current.
func (idx *DB) EnsureReady(ctx context.Context) error {
	lastIndexed, err := idx.lastIndexedBlock()
	if err != nil {
		return fmt.Errorf("txindex: read last indexed block: %w", err)
	}

	latestBlock, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("txindex: get latest block from ImmuDB: %w", err)
	}

	if latestBlock == 0 {
		log.Printf("[txindex] No blocks in ImmuDB yet — nothing to index")
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
func (idx *DB) IndexBlock(block *config.ZKBlock) error {
	if block == nil || len(block.Transactions) == 0 {
		return nil
	}
	return idx.indexBlocks([]*config.ZKBlock{block})
}

// CountByAddress returns the total number of transactions indexed for an address.
// Fast: backed by the idx_address_block index.
func (idx *DB) CountByAddress(address string) (int, error) {
	var n int
	err := idx.db.QueryRow(
		`SELECT COUNT(*) FROM address_txns WHERE address = ?`, strings.ToLower(address),
	).Scan(&n)
	return n, err
}

// GetTxRefsByOffset returns paginated tx references for an address, newest first,
// using SQL OFFSET. Suitable for page-number–based UIs.
func (idx *DB) GetTxRefsByOffset(address string, offset, limit int) ([]TxRef, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}
	rows, err := idx.db.Query(`
		SELECT block_number, tx_hash
		FROM   address_txns
		WHERE  address = ?
		ORDER  BY block_number DESC
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
func (idx *DB) GetTxHashesByAddress(address string, cursor uint64, limit int) ([]TxRef, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}

	var (
		rows *sql.Rows
		err  error
	)

	addr := strings.ToLower(address)
	if cursor == 0 {
		rows, err = idx.db.Query(`
			SELECT block_number, tx_hash
			FROM   address_txns
			WHERE  address = ?
			ORDER  BY block_number DESC
			LIMIT  ?
		`, addr, limit)
	} else {
		rows, err = idx.db.Query(`
			SELECT block_number, tx_hash
			FROM   address_txns
			WHERE  address = ? AND block_number <= ?
			ORDER  BY block_number DESC
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

// Close closes the underlying SQLite connection.
func (idx *DB) Close() error {
	return idx.db.Close()
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

		if err := idx.indexBlocks(blocks); err != nil {
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
func (idx *DB) indexBlocks(blocks []*config.ZKBlock) error {
	if len(blocks) == 0 {
		return nil
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("txindex: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO address_txns (address, block_number, tx_hash)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("txindex: prepare insert: %w", err)
	}
	defer stmt.Close()

	var lastBlock uint64
	for _, blk := range blocks {
		txHash := blk.BlockHash.Hex() // fallback; overridden per-tx below
		for _, t := range blk.Transactions {
			txHash = t.Hash.Hex()

			if t.From != nil {
				if _, err := stmt.Exec(strings.ToLower(t.From.Hex()), blk.BlockNumber, txHash); err != nil {
					return fmt.Errorf("txindex: insert from block %d: %w", blk.BlockNumber, err)
				}
			}
			if t.To != nil {
				if _, err := stmt.Exec(strings.ToLower(t.To.Hex()), blk.BlockNumber, txHash); err != nil {
					return fmt.Errorf("txindex: insert to block %d: %w", blk.BlockNumber, err)
				}
			}
		}
		if blk.BlockNumber > lastBlock {
			lastBlock = blk.BlockNumber
		}
	}

	if err := setMeta(tx, "last_indexed_block", fmt.Sprintf("%d", lastBlock)); err != nil {
		return fmt.Errorf("txindex: update meta: %w", err)
	}

	return tx.Commit()
}

// ── meta helpers ─────────────────────────────────────────────────────────────

func (idx *DB) lastIndexedBlock() (uint64, error) {
	var val string
	err := idx.db.QueryRow(
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

func setMeta(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`
		INSERT INTO index_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// ── singleton ────────────────────────────────────────────────────────────────
// A process-level singleton so callers (broadcast.go, RPC handlers) can reach
// the index without threading *DB through every call site.

var (
	globalIdx     *DB
	globalIdxOnce sync.Once
)

// Init opens the SQLite index, runs EnsureReady, and stores the singleton.
// Must be called once after the ImmuDB pool is ready, before serving RPC.
// Subsequent calls are no-ops.
func Init(ctx context.Context, dbPath string) error {
	var initErr error
	globalIdxOnce.Do(func() {
		idx, err := Open(dbPath)
		if err != nil {
			initErr = fmt.Errorf("txindex.Init: %w", err)
			return
		}
		if err := idx.EnsureReady(ctx); err != nil {
			idx.Close()
			initErr = fmt.Errorf("txindex.Init EnsureReady: %w", err)
			return
		}
		globalIdx = idx
	})
	return initErr
}

// IndexBlockAsync indexes a committed block in the background so it never
// delays the block commit path. Errors are logged, not returned.
func IndexBlockAsync(block *config.ZKBlock) {
	if globalIdx == nil {
		return
	}
	go func() {
		if err := globalIdx.IndexBlock(block); err != nil {
			log.Printf("[txindex] IndexBlock block %d: %v", block.BlockNumber, err)
		}
	}()
}

// EnsureReady re-runs the startup gap check on the singleton index.
// Safe to call at any time (e.g. after fastsync completes) — it is a no-op if
// the index is already current. Returns silently if the singleton is not initialised.
func EnsureReady(ctx context.Context) error {
	if globalIdx == nil {
		return nil
	}
	return globalIdx.EnsureReady(ctx)
}

// RebuildRange re-indexes a specific block range [from, to] regardless of what
// last_indexed_block says. Use this to fill known gaps — e.g. after PoTS writes
// blocks that were missing when the original catchup ran.
// INSERT OR IGNORE means existing rows are safe; no duplicates are created.
func RebuildRange(ctx context.Context, from, to uint64) error {
	if globalIdx == nil {
		return fmt.Errorf("txindex not initialised")
	}
	log.Printf("[txindex] RebuildRange [%d..%d]", from, to)
	if err := globalIdx.buildRange(ctx, from, to); err != nil {
		return fmt.Errorf("txindex: RebuildRange [%d..%d]: %w", from, to, err)
	}
	log.Printf("[txindex] RebuildRange [%d..%d] complete", from, to)
	return nil
}

// RebuildIndex wipes the entire index and re-indexes from genesis.
// Use when gap detection finds the high-water mark is ahead of actual coverage.
// Blocks the caller until complete — run in a goroutine if needed.
func RebuildIndex(ctx context.Context) error {
	if globalIdx == nil {
		return fmt.Errorf("txindex not initialised")
	}
	log.Printf("[txindex] RebuildIndex: wiping and re-indexing from genesis")

	// Truncate both tables inside a single transaction.
	tx, err := globalIdx.db.Begin()
	if err != nil {
		return fmt.Errorf("txindex: RebuildIndex begin: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM address_txns; DELETE FROM index_meta;`); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("txindex: RebuildIndex truncate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("txindex: RebuildIndex commit: %w", err)
	}

	// Re-index everything from ImmuDB.
	return globalIdx.EnsureReady(ctx)
}

// QueryByAddress is the RPC-facing lookup. Returns paginated tx refs.
func QueryByAddress(address string, cursor uint64, limit int) ([]TxRef, error) {
	if globalIdx == nil {
		return nil, fmt.Errorf("txindex not initialised")
	}
	return globalIdx.GetTxHashesByAddress(address, cursor, limit)
}

// QueryByAddressOffset returns a page of tx refs using SQL OFFSET (page-number UIs).
func QueryByAddressOffset(address string, offset, limit int) ([]TxRef, error) {
	if globalIdx == nil {
		return nil, fmt.Errorf("txindex not initialised")
	}
	return globalIdx.GetTxRefsByOffset(address, offset, limit)
}

// CountByAddress returns the total indexed tx count for an address.
func CountByAddress(address string) (int, error) {
	if globalIdx == nil {
		return 0, fmt.Errorf("txindex not initialised")
	}
	return globalIdx.CountByAddress(address)
}
