// MODULE: DB_OPs/thebegateway/outbox_store.go
// PURPOSE: SQLite-backed OutboxStore — persists failed ThebeGateway write payloads
//          for retry by OutboxWorker. WAL for the ThebeDB integration layer.
//
// CORE DATA STRUCTURES:
//   - sqliteOutboxStore: holds *sql.DB (SQLite connection); stateless per-call after init
//   - thebe_outbox table: unbounded rows; entries deleted on Ack; entries with
//     attempts >= MaxOutboxAttempts are skipped by Next() and left for operator inspection
//
// TO MODIFY BEHAVIOR:
//   - Change retry ceiling: update MaxOutboxAttempts constant in types.go
//   - Change backoff formula: edit ExponentialBackoff() in this file
//   - Change storage backend: implement OutboxStore interface with different *sql.DB
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Store request-scoped state on sqliteOutboxStore (stateless by design)
//   - Use string interpolation in SQL queries (parameterized only)
//
// EXTENSION POINT: swap SQLite for another backend → implement OutboxStore interface
//
// CHANGE SCENARIOS:
//   Increase max attempts: change MaxOutboxAttempts in types.go AND update the literal `3`
//     in sqlCreateOutboxIndex DDL — SQLite partial-index filters are SQL literals, not Go
//     constants, so they do not auto-update. Both must be kept in sync manually.
//   Add dead-letter queue: add ListExhausted() method returning entries where attempts >= max

package thebegateway

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Compile-time interface check.
var _ OutboxStore = (*sqliteOutboxStore)(nil)

const (
	sqlCreateOutboxTable = `
        CREATE TABLE IF NOT EXISTS thebe_outbox (
            id            INTEGER PRIMARY KEY AUTOINCREMENT,
            namespace     TEXT    NOT NULL,
            method        TEXT    NOT NULL,
            payload       BLOB    NOT NULL,
            attempts      INTEGER NOT NULL DEFAULT 0,
            next_retry_at INTEGER NOT NULL DEFAULT 0,
            created_at    INTEGER NOT NULL DEFAULT 0
        )`

	sqlCreateOutboxIndex = `
        CREATE INDEX IF NOT EXISTS idx_outbox_next_retry
            ON thebe_outbox(next_retry_at ASC)
            WHERE attempts < 3`

	sqlEnqueue = `
        INSERT INTO thebe_outbox (namespace, method, payload, attempts, next_retry_at, created_at)
        VALUES (?, ?, ?, 0, ?, ?)`

	sqlNext = `
        SELECT id, namespace, method, payload, attempts, next_retry_at, created_at
        FROM thebe_outbox
        WHERE next_retry_at <= ? AND attempts < ?
        ORDER BY next_retry_at ASC
        LIMIT ?`

	sqlAck = `DELETE FROM thebe_outbox WHERE id = ?`

	sqlIncrementAttempts = `
        UPDATE thebe_outbox
        SET attempts = attempts + 1, next_retry_at = ?
        WHERE id = ?`
)

type sqliteOutboxStore struct {
	db *sql.DB
}

// NewOutboxStore opens (or creates) a SQLite database at dbPath, creates the
// thebe_outbox table and index, and returns an OutboxStore.
// Time: O(1) — single DDL round trip on first call
func NewOutboxStore(dbPath string) (OutboxStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("outbox: open sqlite3 at %q: %w", dbPath, err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("outbox: ping sqlite3: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), sqlCreateOutboxTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("outbox: create table: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), sqlCreateOutboxIndex); err != nil {
		db.Close()
		return nil, fmt.Errorf("outbox: create index: %w", err)
	}

	return &sqliteOutboxStore{db: db}, nil
}

// Enqueue inserts a new OutboxEntry into the WAL.
// If entry.NextRetryAt is zero, time.Now() is used.
// Time: O(1) — single INSERT
func (s *sqliteOutboxStore) Enqueue(ctx context.Context, entry OutboxEntry) error {
	nextRetryAt := entry.NextRetryAt
	if nextRetryAt.IsZero() {
		nextRetryAt = time.Now()
	}

	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, sqlEnqueue,
		string(entry.Namespace),
		entry.Method,
		entry.Payload,
		nextRetryAt.Unix(),
		createdAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("outbox: enqueue: %w", err)
	}
	return nil
}

// Next returns up to limit entries ready for retry
// (next_retry_at <= now AND attempts < MaxOutboxAttempts).
// Time: O(limit) — indexed scan by next_retry_at
// DS: idx_outbox_next_retry covers this query exactly
func (s *sqliteOutboxStore) Next(ctx context.Context, limit int) ([]OutboxEntry, error) {
	now := time.Now().Unix()

	rows, err := s.db.QueryContext(ctx, sqlNext, now, MaxOutboxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: next query: %w", err)
	}
	defer rows.Close()

	var entries []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var ns string
		var nextRetryUnix, createdAtUnix int64

		if err := rows.Scan(
			&e.ID,
			&ns,
			&e.Method,
			&e.Payload,
			&e.Attempts,
			&nextRetryUnix,
			&createdAtUnix,
		); err != nil {
			return nil, fmt.Errorf("outbox: next scan: %w", err)
		}

		e.Namespace = Namespace(ns)
		e.NextRetryAt = time.Unix(nextRetryUnix, 0)
		e.CreatedAt = time.Unix(createdAtUnix, 0)
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: next rows: %w", err)
	}

	return entries, nil
}

// Ack deletes the entry by ID after a successful retry.
// Time: O(1) — DELETE by PK
func (s *sqliteOutboxStore) Ack(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, sqlAck, id)
	if err != nil {
		return fmt.Errorf("outbox: ack id=%d: %w", id, err)
	}
	return nil
}

// IncrementAttempts bumps the attempts counter and sets the next retry time.
// Time: O(1) — UPDATE by PK
func (s *sqliteOutboxStore) IncrementAttempts(ctx context.Context, id int64, nextRetryAt time.Time) error {
	_, err := s.db.ExecContext(ctx, sqlIncrementAttempts, nextRetryAt.Unix(), id)
	if err != nil {
		return fmt.Errorf("outbox: increment attempts id=%d: %w", id, err)
	}
	return nil
}

// ExponentialBackoff returns the next retry time for a given attempt count.
// Formula: min(2^attempts seconds, 5 minutes)
// attempts=0 → 1s, attempts=1 → 2s, attempts=2 → 4s (capped at MaxOutboxAttempts=3)
// Time: O(1)
func ExponentialBackoff(attempts int) time.Time {
	delay := min(time.Duration(1<<uint(attempts))*time.Second, 5*time.Minute)
	return time.Now().Add(delay)
}
