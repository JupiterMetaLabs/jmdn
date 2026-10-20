// MODULE: DB_OPs/task_pools.go
// PURPOSE: Task-wise Postgres connection budgets. Each class of work gets its
// OWN *sql.DB with its own MaxOpenConns, so one class saturating its budget
// cannot take connections from another (the accountsdb-pool-exhaustion RCA:
// FastSync account-sync serving pinned the shared pool and starved JSON-RPC
// and block commit).
//
// CLASSES (config: thebe.pools.<class>; env JMDN_THEBE_POOLS_<CLASS>):
//   - write: ThebeDB engine — gateway 2PC writes, outbox drain, contract (cassata)
//            reads. Owned by thebedb.New; sized here, opened in main.go.
//   - read:  the process-wide handle — account/block reads for JSON-RPC, the
//            consensus apply path, explorer, CLI. The default for every caller
//            that passes a nil *config.PooledConnection.
//   - sync:  peer-sync SERVING — FastSync AccountSync/block/header iterators and
//            the ThebeSync provider. Selected explicitly with TaskConn(TaskSync).
//
// ROUTING: a task's handle travels in config.PooledConnection.Handle, which
// getHandle already honors. TaskConn returns nil when a task has no dedicated
// handle registered (tests, Thebe disabled), so routing degrades to the
// process handle instead of failing.
//
// DO NOT:
//   - Share one *sql.DB between two tasks — the isolation is the point.
//   - Route a path to TaskSync unless it is driven by a REMOTE peer's sync
//     request; local consensus apply stays on the read pool.

package DB_OPs

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"

	_ "github.com/lib/pq" // "postgres" driver for OpenTaskDB
)

// DBTask names a connection-budget class.
type DBTask string

const (
	TaskWrite DBTask = "write"
	TaskRead  DBTask = "read"
	TaskSync  DBTask = "sync"
)

var (
	taskMu     sync.RWMutex
	taskConns  = map[DBTask]*config.PooledConnection{}
	taskDBs    = map[DBTask]*sql.DB{}
	taskLimits = map[DBTask]int{}
)

// SetTaskHandle registers the dedicated handle for task t. Passing nil removes
// it (TaskConn(t) then returns nil → process handle).
func SetTaskHandle(t DBTask, h store.ThebeHandle) {
	taskMu.Lock()
	defer taskMu.Unlock()
	if h == nil {
		delete(taskConns, t)
		return
	}
	taskConns[t] = &config.PooledConnection{Handle: h, Database: string(t)}
}

// TaskConn returns the connection that routes DB_OPs calls to task t's pool, or
// nil when t has no dedicated handle (callers then use the process handle).
func TaskConn(t DBTask) *config.PooledConnection {
	taskMu.RLock()
	defer taskMu.RUnlock()
	return taskConns[t]
}

// TaskPoolOptions sizes one task pool.
type TaskPoolOptions struct {
	MaxOpen         int
	MaxIdle         int // <= 0 → ceil(MaxOpen/2)
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func (o TaskPoolOptions) normalized() TaskPoolOptions {
	if o.MaxIdle <= 0 || o.MaxIdle > o.MaxOpen {
		o.MaxIdle = (o.MaxOpen + 1) / 2
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = 2 * time.Hour // ThebeDB pkg/sql default
	}
	if o.ConnMaxIdleTime <= 0 {
		o.ConnMaxIdleTime = 30 * time.Minute // ThebeDB pkg/sql default
	}
	return o
}

// OpenTaskDB opens and pings a dedicated Postgres pool for task t and records it
// for stats. MaxOpen must be >= 1: a 0 limit means UNLIMITED in database/sql,
// which would silently remove the budget.
func OpenTaskDB(ctx context.Context, t DBTask, dsn string, o TaskPoolOptions) (*sql.DB, error) {
	if o.MaxOpen < 1 {
		return nil, fmt.Errorf("db pool %q: max open connections must be >= 1 (got %d)", t, o.MaxOpen)
	}
	o = o.normalized()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("db pool %q: open: %w", t, err)
	}
	db.SetMaxOpenConns(o.MaxOpen)
	db.SetMaxIdleConns(o.MaxIdle)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	db.SetConnMaxIdleTime(o.ConnMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db pool %q: ping: %w", t, err)
	}
	RegisterTaskDB(t, db, o.MaxOpen)
	return db, nil
}

// RegisterTaskDB records an already-open pool (e.g. one wrapped by another
// component) so it is reported by TaskPoolStats.
func RegisterTaskDB(t DBTask, db *sql.DB, maxOpen int) {
	taskMu.Lock()
	defer taskMu.Unlock()
	taskDBs[t] = db
	taskLimits[t] = maxOpen
}

// TaskPoolStat is one pool's live usage.
type TaskPoolStat struct {
	Task    DBTask
	MaxOpen int
	Stats   sql.DBStats
}

// TaskPoolStats returns every registered pool's stats, sorted by task name.
func TaskPoolStats() []TaskPoolStat {
	taskMu.RLock()
	defer taskMu.RUnlock()
	out := make([]TaskPoolStat, 0, len(taskDBs))
	for t, db := range taskDBs {
		out = append(out, TaskPoolStat{Task: t, MaxOpen: taskLimits[t], Stats: db.Stats()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task < out[j].Task })
	return out
}

// TotalTaskConnections sums the configured budgets of the given pools.
func TotalTaskConnections(sizes map[DBTask]int) int {
	n := 0
	for _, v := range sizes {
		n += v
	}
	return n
}

// CheckServerConnectionBudget compares the sum of task budgets with the
// server's max_connections minus superuser_reserved_connections. It returns a
// non-nil error describing the shortfall; the caller decides whether to fail
// or warn (other clients — CDC, psql, other services — also share the server).
func CheckServerConnectionBudget(ctx context.Context, db *sql.DB, total int) error {
	var maxConns, reserved int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('max_connections')::int, current_setting('superuser_reserved_connections')::int`).Scan(&maxConns, &reserved); err != nil {
		return fmt.Errorf("read max_connections: %w", err)
	}
	if avail := maxConns - reserved; total > avail {
		return fmt.Errorf("task pools need %d connections but the server allows %d (max_connections %d - reserved %d)", total, avail, maxConns, reserved)
	}
	return nil
}
