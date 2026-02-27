package config

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ================================================================
// PostgreSQL Connection Pool
// ================================================================

// PostgresPoolConfig holds configuration for the PostgreSQL connection pool.
type PostgresPoolConfig struct {
	DSN            string        // e.g. "postgres://user:pass@localhost:5432/jmdn?sslmode=disable"
	MaxConnections int32         // Maximum number of connections in the pool
	MinConnections int32         // Minimum idle connections to maintain
	MaxConnLife    time.Duration // Maximum lifetime of a connection
	MaxConnIdle    time.Duration // Maximum idle time before a connection is closed
	ConnectTimeout time.Duration // Timeout for establishing new connections
}

// DefaultPostgresPoolConfig returns sensible defaults.
func DefaultPostgresPoolConfig() *PostgresPoolConfig {
	return &PostgresPoolConfig{
		MaxConnections: 20,
		MinConnections: 2,
		MaxConnLife:    30 * time.Minute,
		MaxConnIdle:    5 * time.Minute,
		ConnectTimeout: 10 * time.Second,
	}
}

// PostgresPool wraps a pgxpool.Pool with health check and lifecycle management.
type PostgresPool struct {
	Pool   *pgxpool.Pool
	Config *PostgresPoolConfig
	mu     sync.RWMutex
	closed bool
}

// NewPostgresPool creates and validates a new PostgreSQL connection pool.
func NewPostgresPool(ctx context.Context, cfg *PostgresPoolConfig) (*PostgresPool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("postgres: DSN is required")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: invalid DSN: %w", err)
	}

	// Apply pool settings
	poolCfg.MaxConns = cfg.MaxConnections
	poolCfg.MinConns = cfg.MinConnections
	poolCfg.MaxConnLifetime = cfg.MaxConnLife
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdle

	// Create the pool
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: failed to create pool: %w", err)
	}

	// Verify connectivity
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping failed: %w", err)
	}

	return &PostgresPool{
		Pool:   pool,
		Config: cfg,
	}, nil
}

// Ping checks if the PostgreSQL connection is healthy.
func (p *PostgresPool) Ping(ctx context.Context) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return fmt.Errorf("postgres: pool is closed")
	}
	return p.Pool.Ping(ctx)
}

// Stats returns current pool statistics.
func (p *PostgresPool) Stats() *pgxpool.Stat {
	return p.Pool.Stat()
}

// Close gracefully shuts down the PostgreSQL pool.
func (p *PostgresPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}
	p.closed = true
	p.Pool.Close()
}

// ================================================================
// PebbleDB Connection (Embedded — no pooling needed)
// ================================================================

// PebbleConfig holds configuration for PebbleDB.
type PebbleConfig struct {
	DataDir      string // Directory path for PebbleDB data files
	CacheSize    int64  // LRU cache size in bytes (0 = default 8MB)
	MaxOpenFiles int    // Max number of open files (0 = default 500)
}

// DefaultPebbleConfig returns sensible defaults for PebbleDB.
func DefaultPebbleConfig() *PebbleConfig {
	return &PebbleConfig{
		CacheSize:    64 * 1024 * 1024, // 64MB cache
		MaxOpenFiles: 500,
	}
}

// PebblePool wraps a pebble.DB instance with lifecycle management.
// Note: PebbleDB is embedded, so there is no "pool" — this is a single
// database handle that supports concurrent reads and writes natively.
type PebblePool struct {
	DB     *pebble.DB
	Config *PebbleConfig
	mu     sync.RWMutex
	closed bool
}

// NewPebblePool opens a PebbleDB instance at the configured data directory.
func NewPebblePool(cfg *PebbleConfig) (*PebblePool, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("pebble: DataDir is required")
	}

	opts := &pebble.Options{}

	if cfg.CacheSize > 0 {
		opts.Cache = pebble.NewCache(cfg.CacheSize)
		defer opts.Cache.Unref()
	}

	if cfg.MaxOpenFiles > 0 {
		opts.MaxOpenFiles = cfg.MaxOpenFiles
	}

	db, err := pebble.Open(cfg.DataDir, opts)
	if err != nil {
		return nil, fmt.Errorf("pebble: failed to open database at %s: %w", cfg.DataDir, err)
	}

	return &PebblePool{
		DB:     db,
		Config: cfg,
	}, nil
}

// Ping verifies PebbleDB is operational by performing a no-op read.
func (p *PebblePool) Ping() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return fmt.Errorf("pebble: database is closed")
	}

	// Quick health check — attempt to get a non-existent key
	_, closer, err := p.DB.Get([]byte("__health_check__"))
	if err == pebble.ErrNotFound {
		return nil // Expected — DB is healthy
	}
	if err != nil {
		return fmt.Errorf("pebble: health check failed: %w", err)
	}
	closer.Close()
	return nil
}

// Close gracefully shuts down PebbleDB, flushing all pending writes.
func (p *PebblePool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true
	return p.DB.Close()
}
