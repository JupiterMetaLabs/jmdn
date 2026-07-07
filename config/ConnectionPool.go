package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/config/common"
	"gossipnode/logging"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"go.opentelemetry.io/otel/attribute"
)

var ConnectionPoolLocalGRO interfaces.LocalGoroutineManagerInterface

// LOKI_URL will be set conditionally based on whether Loki is enabled
var LOKI_URL string

const (
	LOG_FILE        = ""
	LOG_DIR         = "logs"
	LOKI_BATCH_SIZE = 128 * 1024
	LOKI_BATCH_WAIT = 1 * time.Second
	LOKI_TIMEOUT    = 5 * time.Second
	KEEP_LOGS       = true
	TOPIC           = "ImmuDB_ConnectionPool"
)

// ConnectionPoolConfig holds configuration for the connection pool
type ConnectionPoolConfig struct {
	MinConnections     int           // Minimum number of connections to maintain
	MaxConnections     int           // Maximum number of connections allowed
	ConnectionTimeout  time.Duration // Timeout for establishing new connections
	IdleTimeout        time.Duration // Time after which idle connections are closed
	MaxLifetime        time.Duration // Maximum lifetime of a connection
	TokenMaxLifetime   time.Duration // Maximum lifetime of a token
	TokenRefreshBuffer time.Duration // How early to refresh tokens before expiry
}

type PoolingConfig struct {
	DBAddress  string
	DBPort     int
	DBName     string
	DBUsername string
	DBPassword string
}

// DefaultConnectionPoolConfig returns default configuration
func DefaultConnectionPoolConfig() *ConnectionPoolConfig {
	return &ConnectionPoolConfig{
		MinConnections:     2,
		MaxConnections:     30,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,
		TokenMaxLifetime:   24 * time.Hour, // Default immudb token lifetime
		TokenRefreshBuffer: 5 * time.Minute,
	}

}

// PooledConnection represents a connection in the pool.
// Client holds a legacy *ImmuClient for old ImmuDB-backed code paths.
// Handle holds a store.ThebeHandle (as io.Closer) for new ThebeDB-backed code.
// Callers using getHandle() will pick up Handle first, then fall back to the
// global process-wide ThebeHandle.
type PooledConnection struct {
	Client    *ImmuClient
	Handle    io.Closer
	Database  string
	CreatedAt time.Time
	LastUsed  time.Time
	InUse     bool
	// Token is the ImmuDB authentication token, used by legacy account_immuclient.go
	// call-sites that set authorization metadata on gRPC requests.
	Token string
}

// HandleFactory creates a new store.ThebeHandle (exposed as io.Closer to avoid import cycle).
// Implementations must return a value that also satisfies store.ThebeHandle.
// Typical implementation: backend.NewComposite(backend.New(gw, r, lw), cache)
type HandleFactory func() (io.Closer, error)

// globalHandleFactory is the process-wide fallback factory used by any pool
// created without an explicit factory. Set once at startup after ThebeDB is
// constructed (config.SetGlobalHandleFactory). Guarded by globalFactoryMu.
var (
	globalHandleFactory HandleFactory
	globalFactoryMu     sync.RWMutex
)

// SetGlobalHandleFactory registers the process-wide ThebeHandle factory.
// Call this exactly once, after ThebeDB (gateway + reader + cache) is built and
// before the first pool connection is borrowed.
func SetGlobalHandleFactory(f HandleFactory) {
	globalFactoryMu.Lock()
	defer globalFactoryMu.Unlock()
	globalHandleFactory = f
}

// GlobalHandleFactory returns the process-wide factory, or nil if unset.
func GlobalHandleFactory() HandleFactory {
	globalFactoryMu.RLock()
	defer globalFactoryMu.RUnlock()
	return globalHandleFactory
}

// ConnectionPool manages a pool of ThebeDB-backed store.ThebeHandle connections.
type ConnectionPool struct {
	Config      *ConnectionPoolConfig
	Connections []*PooledConnection
	Mutex       sync.RWMutex
	Closed      bool
	Logger      *ion.Ion

	Database string

	// factory creates a new ThebeHandle per pool slot.
	// Shared ThebeDB deps (gateway, reader, cache) are captured in the closure at construction time.
	factory HandleFactory

	// Background tasks
	CleanupTicker *time.Ticker
	StopCleanup   chan struct{}
}

// GetContext returns a background context for database operations.
func (pc *PooledConnection) GetContext() context.Context {
	return context.Background()
}

// ImmuClient returns the underlying *ImmuClient if this PooledConnection was
// created with one (legacy ImmuDB pool path). Returns nil when backed only by
// a ThebeDB handle (use getHandle via DB_OPs package instead).
//
// Deprecated: migrate callers to store.ThebeHandle methods.
func (pc *PooledConnection) ImmuClient() *ImmuClient {
	return pc.Client
}

// NewConnectionPool creates a new connection pool backed by ThebeDB.
// factory is called once per pool slot to create a store.ThebeHandle (returned as io.Closer).
func NewConnectionPool(logger_ctx context.Context, config *ConnectionPoolConfig, logger *ion.Ion, poolingConfig *PoolingConfig, factory HandleFactory) *ConnectionPool {
	// Record trace span and close it
	spanCtx, span := logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.NewConnectionPool")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("database", poolingConfig.DBName),
		attribute.Int("max_connections", config.MaxConnections),
		attribute.Int("min_connections", config.MinConnections),
	)

	logger.Debug(spanCtx, "Creating new connection pool",
		ion.String("database", poolingConfig.DBName),
		ion.Int("max_connections", config.MaxConnections),
		ion.Int("min_connections", config.MinConnections),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.NewConnectionPool"))

	if ConnectionPoolLocalGRO == nil {
		var err error
		ConnectionPoolLocalGRO, err = common.InitializeGRO(GRO.ConnectionPoolLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger.Error(spanCtx, "Failed to initialize local GRO",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.NewConnectionPool"))
			logger.Error(spanCtx, "failed to initialize local gro", err)
		}
	}

	pool := &ConnectionPool{
		Config:        config,
		Connections:   make([]*PooledConnection, 0, config.MaxConnections),
		Logger:        logger,
		StopCleanup:   make(chan struct{}),
		CleanupTicker: time.NewTicker(1 * time.Minute),
		Database:      poolingConfig.DBName,
		factory:       factory,
	}

	// Start background cleanup
	if ConnectionPoolLocalGRO != nil {
		ConnectionPoolLocalGRO.Go(GRO.ConnectionPoolThread, func(ctx context.Context) error {
			pool.cleanupRoutine(ctx)
			return nil
		})
	} else {
		go pool.cleanupRoutine(context.Background())
		logger.Warn(spanCtx, "ConnectionPoolLocalGRO is nil, starting cleanup routine in main thread",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.NewConnectionPool"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger.Debug(spanCtx, "New connection pool created successfully",
		ion.String("database", poolingConfig.DBName),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.NewConnectionPool"))

	return pool
}

// Get gets a connection from the pool
func (p *ConnectionPool) Get(logger_ctx context.Context) (*PooledConnection, error) {
	// Record trace span and close it
	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.Get")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("database", p.Database))

	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		span.RecordError(errors.New("connection pool is closed"))
		span.SetAttributes(attribute.String("status", "pool_closed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Error(spanCtx, "Connection pool is closed",
			errors.New("connection pool is closed"),
			ion.String("database", p.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Get"))

		return nil, errors.New("connection pool is closed")
	}

	span.SetAttributes(attribute.Int("pool_size", len(p.Connections)))

	// Iterate backwards to safely remove stale connections while iterating
	for i := len(p.Connections) - 1; i >= 0; i-- {
		conn := p.Connections[i]
		if conn.InUse {
			p.Logger.Debug(spanCtx, "Connection is in use",
				ion.String("database", conn.Database),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.Get"))
			continue
		}

		// Check if connection has exceeded its max lifetime.
		lifetimeExpired := time.Since(conn.CreatedAt) > p.Config.MaxLifetime

		if lifetimeExpired {
			p.closeConnection(spanCtx, conn)
			p.Connections = append(p.Connections[:i], p.Connections[i+1:]...)
			span.SetAttributes(attribute.Bool("lifetime_expired", lifetimeExpired))
			p.Logger.Debug(spanCtx, "Connection closed due to lifetime expiration",
				ion.String("database", conn.Database),
				ion.Bool("lifetime_expired", lifetimeExpired),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.Get"))
			continue
		}

		// Found a valid, idle connection
		conn.InUse = true
		conn.LastUsed = time.Now().UTC()
		span.SetAttributes(
			attribute.String("status", "reused"),
			attribute.Bool("connection_reused", true),
		)
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Debug(spanCtx, "Connection found and reused",
			ion.String("database", conn.Database),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Get"))
		return conn, nil
	}

	// No reusable connection found, check if we can create a new one
	if len(p.Connections) >= p.Config.MaxConnections {
		span.RecordError(errors.New("maximum number of connections reached"))
		span.SetAttributes(attribute.String("status", "max_connections_reached"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Warn(spanCtx, "Maximum number of connections reached",
			ion.Int("current_connections", len(p.Connections)),
			ion.Int("max_connections", p.Config.MaxConnections),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Get"))
		return nil, errors.New("maximum number of connections reached")
	}

	// Create a new connection
	span.SetAttributes(attribute.String("status", "creating_new"))
	conn, err := p.createConnection(spanCtx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "create_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Error(spanCtx, "Failed to create new connection",
			err,
			ion.String("database", p.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Get"))
		return nil, err
	}

	conn.InUse = true
	p.Connections = append(p.Connections, conn)
	span.SetAttributes(
		attribute.String("status", "created"),
		attribute.Bool("connection_created", true),
		attribute.Int("new_pool_size", len(p.Connections)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))
	p.Logger.Debug(spanCtx, "New connection created and added to pool",
		ion.String("database", p.Database),
		ion.Int("pool_size", len(p.Connections)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.Get"))
	return conn, nil
}

// createConnection calls the HandleFactory to build a new store.ThebeHandle (returned as io.Closer).
// No network connection is established here — the ThebeDB deps are pre-connected and captured in factory.
func (cp *ConnectionPool) createConnection(logger_ctx context.Context) (*PooledConnection, error) {
	spanCtx, span := cp.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.createConnection")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("database", cp.Database))

	cp.Logger.Debug(spanCtx, "Creating new ThebeDB handle",
		ion.String("database", cp.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.createConnection"))

	factory := cp.factory
	if factory == nil {
		// Fall back to the process-wide factory set once ThebeDB is constructed.
		// Pools are created lazily (no pre-warm), so the global factory only needs
		// to be set before the first GetConnection — not at InitPool time.
		factory = GlobalHandleFactory()
	}
	if factory == nil {
		err := fmt.Errorf("ConnectionPool.createConnection: HandleFactory is nil (call config.SetGlobalHandleFactory after ThebeDB init)")
		span.RecordError(err)
		return nil, err
	}

	handle, err := factory()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "factory_failed"))
		cp.Logger.Error(spanCtx, "HandleFactory failed",
			err,
			ion.String("database", cp.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.createConnection"))
		return nil, fmt.Errorf("ConnectionPool.createConnection: %w", err)
	}

	now := time.Now().UTC()
	conn := &PooledConnection{
		Handle:    handle,
		Database:  cp.Database,
		CreatedAt: now,
		LastUsed:  now,
		InUse:     false,
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	cp.Logger.Debug(spanCtx, "Successfully created new ThebeDB handle",
		ion.String("database", cp.Database),
		ion.Float64("total_duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.createConnection"))

	return conn, nil
}

// Put returns a connection to the pool
// It is safe to call multiple times - if the connection is already in the pool (InUse == false),
// it will be a no-op to prevent duplicate entries.
func (p *ConnectionPool) Put(logger_ctx context.Context, conn *PooledConnection) {
	// Record trace span and close it
	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.Put")
	defer span.End()

	startTime := time.Now().UTC()

	if conn == nil {
		span.RecordError(errors.New("nil connection"))
		span.SetAttributes(attribute.String("status", "nil_connection"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Warn(spanCtx, "ConnectionPool.Put() called with nil connection",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Put"))
		return
	}

	span.SetAttributes(attribute.String("database", conn.Database))

	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		span.SetAttributes(attribute.String("status", "pool_closed"))
		p.closeConnection(spanCtx, conn)
		return
	}

	// Check if connection is already in the pool (already returned)
	// If InUse is false, the connection is already available in the pool
	if !conn.InUse {
		// Connection is already in the pool, just update LastUsed
		conn.LastUsed = time.Now().UTC()
		span.SetAttributes(attribute.String("status", "already_in_pool"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Debug(spanCtx, "Connection already in pool, updating LastUsed",
			ion.String("database", conn.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.Put"))
		return
	}

	// Check if connection is already in the Connections slice to prevent duplicates
	for _, existingConn := range p.Connections {
		if existingConn == conn {
			// Connection already in pool, just mark as not in use
			conn.InUse = false
			conn.LastUsed = time.Now().UTC()
			span.SetAttributes(attribute.String("status", "already_in_slice"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			p.Logger.Debug(spanCtx, "Connection already in pool slice, marking as not in use",
				ion.String("database", conn.Database),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.Put"))
			return
		}
	}

	// Connection is not in pool, add it
	conn.InUse = false
	conn.LastUsed = time.Now().UTC()

	// Add the connection back to the pool
	p.Connections = append(p.Connections, conn)

	span.SetAttributes(
		attribute.String("status", "returned"),
		attribute.Int("pool_size", len(p.Connections)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))
	p.Logger.Debug(spanCtx, "Connection returned to pool",
		ion.String("database", conn.Database),
		ion.Int("pool_size", len(p.Connections)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.Put"))
}

// Close closes the connection pool and all its connections
func (p *ConnectionPool) Close(logger_ctx context.Context) {
	// Record trace span and close it
	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.Close")
	defer span.End()

	startTime := time.Now().UTC()

	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		span.SetAttributes(attribute.String("status", "already_closed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	p.Closed = true
	close(p.StopCleanup)
	p.CleanupTicker.Stop()

	connectionCount := len(p.Connections)
	span.SetAttributes(
		attribute.Int("connection_count", connectionCount),
		attribute.String("database", p.Database),
	)

	p.Logger.Debug(spanCtx, "Closing connection pool and disconnecting connections",
		ion.Int("connection_count", connectionCount),
		ion.String("database", p.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.Close"))

	for _, conn := range p.Connections {
		p.closeConnection(spanCtx, conn)
	}

	p.Connections = nil

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	p.Logger.Debug(spanCtx, "Connection pool closed successfully",
		ion.Int("connection_count", connectionCount),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.Close"))
}

// closeConnection releases the handle's resources.
func (p *ConnectionPool) closeConnection(logger_ctx context.Context, conn *PooledConnection) {
	if conn == nil {
		return
	}

	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.closeConnection")
	defer span.End()

	span.SetAttributes(attribute.String("database", conn.Database))

	// Close the ThebeDB handle if present.
	if conn.Handle != nil {
		_ = conn.Handle.Close()
	}
	// Close the legacy ImmuDB client if present.
	if conn.Client != nil {
		_ = conn.Client.Close()
	}

	p.Logger.Debug(spanCtx, "Closed ThebeDB handle",
		ion.String("database", conn.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.closeConnection"))

	span.SetAttributes(attribute.String("status", "success"))
}

// cleanupRoutine periodically cleans up idle connections.
// It stops when ctx is cancelled or StopCleanup is closed.
func (p *ConnectionPool) cleanupRoutine(ctx context.Context) {
	// Record trace span and close it
	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(ctx, "ConnectionPool.cleanupRoutine")
	defer span.End()

	span.SetAttributes(attribute.String("database", p.Database))

	p.Logger.Debug(spanCtx, "Starting connection pool cleanup routine",
		ion.String("database", p.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.cleanupRoutine"))

	for {
		select {
		case <-ctx.Done():
			span.SetAttributes(attribute.String("status", "ctx_cancelled"))
			p.Logger.Debug(spanCtx, "Stopping connection pool cleanup routine (ctx cancelled)",
				ion.String("database", p.Database),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.cleanupRoutine"))
			return
		case <-p.CleanupTicker.C:
			p.cleanupIdleConnections(spanCtx)
		case <-p.StopCleanup:
			span.SetAttributes(attribute.String("status", "stop_requested"))
			p.Logger.Debug(spanCtx, "Stopping connection pool cleanup routine",
				ion.String("database", p.Database),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ConnectionPool.cleanupRoutine"))
			return
		}
	}
}

// cleanupIdleConnections removes idle connections that have exceeded their max lifetime
func (p *ConnectionPool) cleanupIdleConnections(logger_ctx context.Context) {
	// Record trace span and close it
	spanCtx, span := p.Logger.Tracer("ConnectionPool").Start(logger_ctx, "ConnectionPool.cleanupIdleConnections")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("database", p.Database))

	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		span.SetAttributes(attribute.String("status", "pool_closed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	var active []*PooledConnection
	now := time.Now().UTC()
	closedCount := 0

	for _, conn := range p.Connections {
		if conn.InUse {
			active = append(active, conn)
			continue
		}

		// Close connections that are too old or have been idle too long
		lifetimeExpired := now.Sub(conn.CreatedAt) > p.Config.MaxLifetime
		idleExpired := now.Sub(conn.LastUsed) > p.Config.IdleTimeout

		if lifetimeExpired || idleExpired {
			p.closeConnection(spanCtx, conn)
			closedCount++
			continue
		}

		active = append(active, conn)
	}

	if closedCount > 0 {
		p.Connections = active
		span.SetAttributes(
			attribute.Int("closed_count", closedCount),
			attribute.Int("remaining_connections", len(p.Connections)),
			attribute.String("status", "connections_cleaned"),
		)
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		p.Logger.Debug(spanCtx, "Cleaned up idle/stale connections",
			ion.Int("closed_count", closedCount),
			ion.Int("remaining_connections", len(p.Connections)),
			ion.String("database", p.Database),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ConnectionPool.cleanupIdleConnections"))
	} else {
		span.SetAttributes(attribute.String("status", "no_cleanup_needed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
	}
}

// Global connection pool - this will be used by existing functions
var (
	globalPool     *ConnectionPool
	globalPoolOnce sync.Once
)

// InitGlobalPool initializes the global connection pool. It must be called once
// at application startup before any calls to GetGlobalPool.
// It panics if initialization fails, as a DB connection pool is a critical
// component for the application to run.
// factory: called per pool slot to create a store.ThebeHandle (as io.Closer).
func InitGlobalPool(logger_ctx context.Context, poolConfig *PoolingConfig, logger *ion.Ion, factory HandleFactory) {
	InitGlobalPoolWithLoki(poolConfig, factory)
}

// InitGlobalPoolWithLoki initializes the global connection pool with optional Loki support.
// factory: called per pool slot to build a store.ThebeHandle (returned as io.Closer).
func InitGlobalPoolWithLoki(poolConfig *PoolingConfig, factory HandleFactory) {
	asyncLogger := logging.NewAsyncLogger()
	if asyncLogger == nil {
		panic("FATAL: NewAsyncLogger returned nil")
	}

	asyncLoggerInstance := asyncLogger.Get()
	if asyncLoggerInstance == nil {
		panic("FATAL: AsyncLogger.Get() returned nil - GlobalLogger not initialized")
	}

	loggerInstance, err := asyncLoggerInstance.NamedLogger(logging.MainDB_Connections, "")
	if err != nil {
		panic(fmt.Sprintf("FATAL: failed to get named logger for InitGlobalPool: %v", err))
	}

	logger := loggerInstance.NamedLogger

	logger_ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	globalPoolOnce.Do(func() {
		if poolConfig == nil {
			panic("FATAL: poolConfig cannot be nil for InitGlobalPool")
		}
		if logger == nil {
			panic("FATAL: logger cannot be nil for InitGlobalPool")
		}
		poolCfg := DefaultConnectionPoolConfig()
		globalPool = NewConnectionPool(logger_ctx, poolCfg, logger, poolConfig, factory)
	})
}

// GetGlobalPool returns the global connection pool instance.
// It will panic if InitGlobalPool has not been called first.
func GetGlobalPool(logger_ctx context.Context) *ConnectionPool {
	if globalPool == nil {
		panic("FATAL: global connection pool is not initialized. Call InitGlobalPool first.")
	}
	globalPool.Logger.Debug(logger_ctx, "Global connection pool retrieved",
		ion.String("database", globalPool.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ConnectionPool.GetGlobalPool"))
	return globalPool
}
