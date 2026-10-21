package config

import (
	"context"
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
	TOPIC           = "ThebeDB_ConnectionPool"
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
	// DBName is a logical label used in logs; ThebeDB is an embedded store
	// so no network address or credentials are required.
	DBName string
}

// DefaultConnectionPoolConfig returns default configuration
func DefaultConnectionPoolConfig() *ConnectionPoolConfig {
	return &ConnectionPoolConfig{
		MinConnections:     2,
		MaxConnections:     30,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,
		TokenMaxLifetime:   24 * time.Hour, // legacy default token lifetime (unused on the Thebe path)
		TokenRefreshBuffer: 5 * time.Minute,
	}

}

// PooledConnection represents a connection in the pool.
// Handle holds a store.ThebeHandle (as io.Closer) for ThebeDB-backed code.
// Callers using getHandle() will pick up Handle first, then fall back to the
// global process-wide ThebeHandle.
type PooledConnection struct {
	Handle    io.Closer
	Database  string
	CreatedAt time.Time
	LastUsed  time.Time
	InUse     bool
}

// HandleFactory creates a new store.ThebeHandle (exposed as io.Closer to avoid import cycle).
// Implementations must return a value that also satisfies store.ThebeHandle.
// Typical implementation: backend.NewComposite(backend.New(gw, r, lw), cache)
type HandleFactory func() (io.Closer, error)

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
	// Close the legacy client handle if present.

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
