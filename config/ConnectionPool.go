package config

import (
	"context"
	"errors"
	"fmt"
	"gossipnode/logging"
	"os"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

var LOKI_URL = logging.GetLokiURL()

const (
	LOG_FILE        = "ImmuDB.log"
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
		MaxConnections:     20,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,
		TokenMaxLifetime:   24 * time.Hour, // Default immudb token lifetime
		TokenRefreshBuffer: 5 * time.Minute,
	}

}

// PooledConnection represents a connection in the pool
type PooledConnection struct {
	Client      *ImmuClient
	Token       string
	TokenExpiry time.Time
	Database    string
	CreatedAt   time.Time
	LastUsed    time.Time
	InUse       bool
}

// ConnectionPool manages a pool of ImmuDB connections
type ConnectionPool struct {
	Config      *ConnectionPoolConfig
	Connections []*PooledConnection
	Mutex       sync.RWMutex
	Closed      bool
	Logger      *logging.AsyncLogger

	// Connection details (using default ImmuDB values)
	Address  string // Name of the ImmuDB server
	Port     int
	Database string
	Username string
	Password string

	// Background tasks
	CleanupTicker *time.Ticker
	StopCleanup   chan struct{}
}

// NewAsyncLogger creates a new async logger using the centralized logging package
func NewAsyncLogger() (*logging.AsyncLogger, error) {
	LogStruct := &logging.Logging{
		FileName: LOG_FILE,
		URL:      LOKI_URL,
		Metadata: logging.LoggingMetadata{
			DIR:       LOG_DIR,
			BatchSize: LOKI_BATCH_SIZE,
			BatchWait: LOKI_BATCH_WAIT,
			Timeout:   LOKI_TIMEOUT,
			KeepLogs:  KEEP_LOGS,
		},
		Topic: TOPIC,
	}

	// Use the logging package to create a new logger
	logger, err := logging.NewAsyncLogger(LogStruct)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}
	logger.Logger.Info("Logger initialized",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.NewAsyncLogger"),
	)
	return logger, nil
}

// GetContext returns a new background context with the connection's authentication token.
// Callers should use this to create contexts for their database operations.
func (pc *PooledConnection) GetContext() context.Context {
	return metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", pc.Token))
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(config *ConnectionPoolConfig, logger *logging.AsyncLogger, poolingConfig *PoolingConfig) *ConnectionPool {
	pool := &ConnectionPool{
		Config:        config,
		Connections:   make([]*PooledConnection, 0, config.MaxConnections),
		Logger:        logger,
		StopCleanup:   make(chan struct{}),
		CleanupTicker: time.NewTicker(1 * time.Minute),
		Address:       poolingConfig.DBAddress,
		Port:          poolingConfig.DBPort,
		Database:      poolingConfig.DBName,
		Username:      poolingConfig.DBUsername,
		Password:      poolingConfig.DBPassword,
	}

	// Start background cleanup
	go pool.cleanupRoutine()
	logger.Logger.Info("New Connection Pool Created",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.NewConnectionPool"),
	)

	return pool
}

// Get gets a connection from the pool
func (p *ConnectionPool) Get() (*PooledConnection, error) {
	fmt.Println("ConnectionPool.Get() called - acquiring lock...")
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	fmt.Printf("ConnectionPool.Get() - lock acquired, checking for available connections... (pool has %d connections)\n", len(p.Connections))

	if p.Closed {
		p.Logger.Logger.Info("Connection pool is closed",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "config.Get"),
		)

		return nil, errors.New("connection pool is closed")
	}

	// Iterate backwards to safely remove stale connections while iterating
	for i := len(p.Connections) - 1; i >= 0; i-- {
		conn := p.Connections[i]
		fmt.Printf("Checking connection %d: InUse=%v, CreatedAt=%v, LastUsed=%v\n", i, conn.InUse, conn.CreatedAt, conn.LastUsed)
		if conn.InUse {
			p.Logger.Logger.Info("Connection is in use",
				zap.Time(logging.Created_at, time.Now()),
				zap.Time(logging.Connection_created_at, conn.CreatedAt),
				zap.Time(logging.Connection_last_used, conn.LastUsed),
				zap.String(logging.Connection_id, conn.Token),
				zap.String(logging.Connection_database, conn.Database),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "config.Get"),
			)
			continue
		}

		// Check if connection is expired (lifetime or token)
		lifetimeExpired := time.Since(conn.CreatedAt) > p.Config.MaxLifetime
		tokenExpired := time.Until(conn.TokenExpiry) < p.Config.TokenRefreshBuffer

		if lifetimeExpired || tokenExpired {
			p.closeConnection(conn)
			p.Connections = append(p.Connections[:i], p.Connections[i+1:]...)
			p.Logger.Logger.Info("Connection closed due to expiration",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Connection_id, conn.Token),
				zap.String(logging.Connection_database, conn.Database),
				zap.Time(logging.Connection_created_at, conn.CreatedAt),
				zap.Time(logging.Connection_last_used, conn.LastUsed),
				zap.Bool("lifetime_expired", lifetimeExpired),
				zap.Bool("token_expired", tokenExpired),
				zap.Duration("time_until_token_expiry", time.Until(conn.TokenExpiry)),
				zap.Duration("token_refresh_buffer", p.Config.TokenRefreshBuffer),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "config.Get"),
			)
			continue
		}

		// Found a valid, idle connection
		conn.InUse = true
		conn.LastUsed = time.Now()
		p.Logger.Logger.Info("Connection found",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Connection_id, conn.Token),
			zap.String(logging.Connection_database, conn.Database),
			zap.Time(logging.Connection_created_at, conn.CreatedAt),
			zap.Time(logging.Connection_last_used, conn.LastUsed),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "config.Get"),
		)
		return conn, nil
	}

	// No reusable connection found, check if we can create a new one
	if len(p.Connections) >= p.Config.MaxConnections {
		p.Logger.Logger.Info("Maximum number of connections reached",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "config.Get"),
		)
		return nil, errors.New("maximum number of connections reached")
	}

	// Create a new connection
	fmt.Println("No available connections, creating new connection...")
	var conn *PooledConnection
	var err error
	conn, err = p.createConnection()
	if err != nil {
		fmt.Printf("Failed to create new connection: %v\n", err)
		return nil, err
	}
	fmt.Println("New connection created successfully")

	conn.InUse = true
	p.Connections = append(p.Connections, conn)
	return conn, nil
}

// createConnection creates a new connection to ImmuDB
func (cp *ConnectionPool) createConnection() (*PooledConnection, error) {
	fmt.Println("createConnection() called - creating new ImmuDB connection...")
	cp.Logger.Logger.Info("Creating new connection to ImmuDB at %s:%d",
		zap.String(logging.Address, cp.Address),
		zap.Int(logging.Port, cp.Port),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.Time(logging.Connection_created_at, time.Now()),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.createConnection"),
	)
	// ensure our state dir exists
	fmt.Println("Ensuring state directory exists...")
	if err := os.MkdirAll(State_Path_Hidden, 0o755); err != nil {
		return nil, fmt.Errorf("could not create state dir: %w", err)
	}
	fmt.Println("State directory exists, building certificate paths...")

	// Configure the client - disable mTLS for local development
	fmt.Println("Configuring ImmuDB client options...")
	opts := client.DefaultOptions().
		WithAddress(cp.Address).
		WithPort(cp.Port).
		WithMTLs(false) // Disable mTLS for local development
	fmt.Println("Client options configured successfully")

	fmt.Println("Creating ImmuDB client with TLS options...")
	c, err := client.NewImmuClient(opts)
	if err != nil {
		fmt.Printf("Failed to create ImmuDB client: %v\n", err)
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	fmt.Println("ImmuDB client created successfully", c.SessionID)

	// login + select database as before
	ctx, cancel := context.WithTimeout(context.Background(), cp.Config.ConnectionTimeout)
	defer cancel()

	cp.Logger.Logger.Info("Authenticating with ImmuDB as user '%s'",
		zap.String(logging.Username, cp.Username),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.createConnection"),
	)
	lr, err := c.Login(ctx, []byte(cp.Username), []byte(cp.Password))
	if err != nil {
		c.Disconnect()
		return nil, fmt.Errorf("login failed: %w", err)
	}

	md := metadata.Pairs("authorization", lr.Token)
	authCtx := metadata.NewOutgoingContext(ctx, md)

	cp.Logger.Logger.Info("Selecting database: %s",
		zap.String(logging.Connection_database, cp.Database),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.createConnection"),
	)
	dbResp, err := c.UseDatabase(authCtx, &schema.Database{DatabaseName: cp.Database})
	if err != nil {
		c.Disconnect()
		return nil, fmt.Errorf("failed to use database %s: %w", cp.Database, err)
	}

	Immuclient := ImmuClient{
		Client:      c,
		Ctx:         ctx,
		Cancel:      cancel,
		BaseCtx:     context.Background(),
		Database:    cp.Database,
		RetryLimit:  3,
		IsConnected: true,
		Logger:      cp.Logger,
	}

	now := time.Now()
	conn := &PooledConnection{
		Client:      &Immuclient,
		Token:       dbResp.Token,
		TokenExpiry: now.Add(cp.Config.TokenMaxLifetime),
		Database:    cp.Database,
		CreatedAt:   now,
		LastUsed:    now,
		InUse:       false,
	}

	cp.Logger.Logger.Info("Successfully created new connection to database: %s",
		zap.String(logging.Connection_database, cp.Database),
		zap.String(logging.Connection_id, conn.Token),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.createConnection"),
	)
	return conn, nil
}

// Put returns a connection to the pool
func (p *ConnectionPool) Put(conn *PooledConnection) {
	if conn == nil {
		fmt.Println("ConnectionPool.Put() called with nil connection")
		return
	}

	fmt.Printf("ConnectionPool.Put() called - returning connection to pool (pool will have %d connections after this)\n", len(p.Connections))
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		p.closeConnection(conn)
		return
	}

	conn.InUse = false
	conn.LastUsed = time.Now()

	// Add the connection back to the pool
	p.Connections = append(p.Connections, conn)

	p.Logger.Logger.Info("Connection returned to pool",
		zap.String(logging.Connection_id, conn.Token),
		zap.String(logging.Connection_database, conn.Database),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.Put"),
	)
}

// Close closes the connection pool and all its connections
func (p *ConnectionPool) Close() {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		return
	}

	p.Closed = true
	close(p.StopCleanup)
	p.CleanupTicker.Stop()

	p.Logger.Logger.Info("Closing connection pool and disconnecting %d connections.",
		zap.Int(logging.ConnectionCount, len(p.Connections)),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.Close"),
	)

	for _, conn := range p.Connections {
		p.closeConnection(conn)
	}

	p.Connections = nil
}

// closeConnection safely disconnects the client.
func (p *ConnectionPool) closeConnection(conn *PooledConnection) {
	if conn == nil || conn.Client == nil {
		return
	}
	conn.Client.Cancel()

	// Log the disconnection
	p.Logger.Logger.Info("Disconnected ImmuDB client",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.closeConnection"),
	)
}

// cleanupRoutine periodically cleans up idle connections
func (p *ConnectionPool) cleanupRoutine() {
	p.Logger.Logger.Info("Starting connection pool cleanup routine",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.cleanupRoutine"),
	)
	for {
		select {
		case <-p.CleanupTicker.C:
			p.cleanupIdleConnections()
		case <-p.StopCleanup:
			p.Logger.Logger.Info("Stopping connection pool cleanup routine",
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "config.cleanupRoutine"),
			)
			return
		}
	}
}

// cleanupIdleConnections removes idle connections that have exceeded their max lifetime
func (p *ConnectionPool) cleanupIdleConnections() {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	if p.Closed {
		return
	}

	var active []*PooledConnection
	now := time.Now()
	closedCount := 0

	for _, conn := range p.Connections {
		if conn.InUse {
			active = append(active, conn)
			continue
		}

		// Close connections that are too old or have been idle too long
		if now.Sub(conn.CreatedAt) > p.Config.MaxLifetime || now.Sub(conn.LastUsed) > p.Config.IdleTimeout {
			p.closeConnection(conn)
			closedCount++
			continue
		}

		active = append(active, conn)
	}

	if closedCount > 0 {
		p.Connections = active
		if p.Logger != nil && p.Logger.Logger != nil {
			p.Logger.Logger.Info("Cleaned up %d idle/stale connections",
				zap.Int(logging.ConnectionCount, closedCount),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "config.cleanupIdleConnections"),
			)
		}
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
func InitGlobalPool(poolConfig *PoolingConfig) {
	globalPoolOnce.Do(func() {
		if poolConfig == nil {
			panic("FATAL: poolConfig cannot be nil for InitGlobalPool")
		}
		poolCfg := DefaultConnectionPoolConfig()
		logger, err := NewAsyncLogger()
		if err != nil {
			// If we can't create a logger, the application is in an unrecoverable state.
			logger.Logger.Error("FATAL: could not create logger for connection pool: %v",
				zap.String("error", err.Error()),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "config.InitGlobalPool"),
			)
			panic(fmt.Sprintf("FATAL: could not create logger for connection pool: %v", err))
		}
		globalPool = NewConnectionPool(poolCfg, logger, poolConfig)
	})
}

// GetGlobalPool returns the global connection pool instance.
// It will panic if InitGlobalPool has not been called first.
func GetGlobalPool() *ConnectionPool {
	if globalPool == nil {
		panic("FATAL: global connection pool is not initialized. Call InitGlobalPool first.")
	}
	globalPool.Logger.Logger.Info("Global connection pool retrieved",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "config.GetGlobalPool"),
	)
	return globalPool
}
