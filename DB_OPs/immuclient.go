package DB_OPs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"

	"strings"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Custom errors
var (
	ErrEmptyKey        = errors.New("key cannot be empty")
	ErrEmptyBatch      = errors.New("entries map cannot be empty")
	ErrNilValue        = errors.New("value cannot be nil")
	ErrNotFound        = errors.New("key not found")
	ErrConnectionLost  = errors.New("connection to immudb lost")
	ErrPoolClosed      = errors.New("connection pool is closed")
	ErrTokenExpired    = errors.New("authentication token expired")
	ErrNoAvailableConn = errors.New("no available connections in pool")
)


// InitializeGlobalPool initializes the global connection pool
func InitializeGlobalPool(poolConfig *config.ConnectionPoolConfig) error {
	config.poolMutex.Lock()
	defer config.poolMutex.Unlock()

	if config.globalPool != nil {
		return nil // Already initialized
	}

	configForPool := &PoolingConfig{
		DBAddress: config.DBAddress,
		DBPort: config.DBPort,
		DBName: config.DBName,
		DBUsername: config.DBUsername,
		DBPassword: config.DBPassword,
	}

	globalPool = GetGlobalPool(configForPool)

	// Initialize minimum connections
	if err := initializePoolConnections(globalPool); err != nil {
		globalPool.Close()
		globalPool = nil
		return err
	}

	return nil
}



// initializePoolConnections creates the minimum number of connections
func initializePoolConnections(pool *ConnectionPool) error {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	for i := 0; i < pool.config.MinConnections; i++ {
		conn, err := pool.createConnection()
		if err != nil {
			return fmt.Errorf("failed to initialize connection %d: %w", i+1, err)
		}
		pool.connections = append(pool.connections, conn)
	}

	config.Info(pool.logger, "Initialized connection pool with %d connections", pool.config.MinConnections)
	return nil
}

// startCleanupRoutine starts the background cleanup of idle connections
func (cp *ConnectionPool) startCleanupRoutine() {
	cp.cleanupTicker = time.NewTicker(1 * time.Minute)
	cp.wg.Add(1)

	go func() {
		defer cp.wg.Done()
		for {
			select {
			case <-cp.cleanupTicker.C:
				cp.cleanupIdleConnections()
			case <-cp.stopCleanup:
				return
			}
		}
	}()
}

// cleanupIdleConnections removes idle and expired connections
func (cp *ConnectionPool) cleanupIdleConnections() {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	if cp.closed {
		return
	}

	now := time.Now()
	activeConnections := make([]*PooledConnection, 0, len(cp.connections))

	for _, conn := range cp.connections {
		shouldRemove := false

		// Remove if connection is too old
		if now.Sub(conn.CreatedAt) > cp.config.MaxLifetime {
			shouldRemove = true
			config.Info(cp.logger, "Removing connection due to max lifetime exceeded")
		}

		// Remove if connection has been idle too long (and we have more than minimum)
		if !conn.InUse && now.Sub(conn.LastUsed) > cp.config.IdleTimeout && len(activeConnections) >= cp.config.MinConnections {
			shouldRemove = true
			config.Info(cp.logger, "Removing idle connection")
		}

		if shouldRemove {
			cp.closeConnection(conn)
		} else {
			activeConnections = append(activeConnections, conn)
		}
	}

	cp.connections = activeConnections
}

// closeConnection safely closes a connection
func (cp *ConnectionPool) closeConnection(conn *PooledConnection) {
	if conn.Cancel != nil {
		conn.Cancel()
	}
	if conn.Client != nil {
		conn.Client.Disconnect()
	}
}

// getConnection gets an available connection from the pool
func (cp *ConnectionPool) GetConnection() (*PooledConnection, error) {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	if cp.closed {
		return nil, ErrPoolClosed
	}

	// Look for an available connection
	for _, conn := range cp.connections {
		if !conn.InUse {
			// Check if token needs refresh
			if cp.needsTokenRefresh(conn) {
				if err := cp.refreshConnectionToken(conn); err != nil {
					config.Warning(cp.logger, "Failed to refresh token for connection: %v", err)
					continue
				}
			}

			conn.InUse = true
			conn.LastUsed = time.Now()
			return conn, nil
		}
	}

	// If no available connection and we can create more
	if len(cp.connections) < cp.config.MaxConnections {
		conn, err := cp.createConnection()
		if err != nil {
			return nil, err
		}

		conn.InUse = true
		cp.connections = append(cp.connections, conn)
		return conn, nil
	}

	return nil, ErrNoAvailableConn
}

// needsTokenRefresh checks if a connection's token needs to be refreshed
func (cp *ConnectionPool) needsTokenRefresh(conn *PooledConnection) bool {
	return time.Until(conn.TokenExpiry) < cp.config.TokenRefreshBuffer
}

// refreshConnectionToken refreshes the authentication token for a connection
func (cp *ConnectionPool) refreshConnectionToken(conn *PooledConnection) error {
	config.Info(cp.logger, "Refreshing authentication token")

	// Create new context for login
	ctx, cancel := context.WithTimeout(context.Background(), cp.config.ConnectionTimeout)
	defer cancel()

	// Re-authenticate with default credentials
	lr, err := conn.Client.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		return fmt.Errorf("token refresh login failed: %w", err)
	}

	// Update context with new token
	md := metadata.Pairs("authorization", lr.Token)
	authCtx := metadata.NewOutgoingContext(context.Background(), md)

	// Re-select database to get database-specific token
	dbResp, err := conn.Client.UseDatabase(authCtx, &schema.Database{DatabaseName: conn.Database})
	if err != nil {
		return fmt.Errorf("failed to re-select database during token refresh: %w", err)
	}

	// Update connection with new token and context
	conn.Token = dbResp.Token
	conn.TokenExpiry = time.Now().Add(24 * time.Hour)

	md = metadata.Pairs("authorization", dbResp.Token)
	conn.Ctx = metadata.NewOutgoingContext(context.Background(), md)

	config.Info(cp.logger, "Successfully refreshed authentication token")
	return nil
}

// releaseConnection returns a connection to the pool
func (cp *ConnectionPool) releaseConnection(conn *PooledConnection) {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	if cp.closed {
		cp.closeConnection(conn)
		return
	}

	conn.InUse = false
	conn.LastUsed = time.Now()
}

// Close closes all connections in the pool
func (cp *ConnectionPool) Close() error {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	if cp.closed {
		return nil
	}

	cp.closed = true

	// Stop cleanup routine
	close(cp.stopCleanup)
	if cp.cleanupTicker != nil {
		cp.cleanupTicker.Stop()
	}

	// Close all connections
	for _, conn := range cp.connections {
		cp.closeConnection(conn)
	}

	cp.connections = nil

	// Wait for cleanup routine to finish
	cp.wg.Wait()

	config.Info(cp.logger, "Connection pool closed successfully")
	return nil
}

// GetPoolStats returns statistics about the connection pool
func (cp *ConnectionPool) GetPoolStats() map[string]interface{} {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	totalConnections := len(cp.connections)
	inUseConnections := 0

	for _, conn := range cp.connections {
		if conn.InUse {
			inUseConnections++
		}
	}

	return map[string]interface{}{
		"total_connections":     totalConnections,
		"in_use_connections":    inUseConnections,
		"available_connections": totalConnections - inUseConnections,
		"max_connections":       cp.config.MaxConnections,
		"min_connections":       cp.config.MinConnections,
		"pool_closed":           cp.closed,
	}
}

// withPooledRetry executes the given operation with retry logic using connection pool
func withPooledRetry(operation string, fn func(*PooledConnection) error) error {
	pool := GetGlobalPool()
	var err error

	retryLimit := 3 // Default retry limit

	for attempt := 0; attempt <= retryLimit; attempt++ {
		// Get connection from pool
		conn, connErr := pool.GetConnection()
		if connErr != nil {
			config.Error(pool.logger, "Failed to get connection from pool for %s (attempt %d/%d): %v",
				operation, attempt+1, retryLimit+1, connErr)
			if attempt == retryLimit {
				return fmt.Errorf("%s failed: %w", operation, connErr)
			}
			time.Sleep(time.Second * time.Duration(attempt+1))
			continue
		}

		// Execute the operation
		err = fn(conn)

		// Always release the connection back to pool
		pool.releaseConnection(conn)

		// If successful, return nil
		if err == nil {
			return nil
		}

		// Check if error is due to connection issues or token expiration
		if isConnectionError(err) || isTokenExpiredError(err) {
			config.Warning(pool.logger, "%s operation failed due to connection/token issue (attempt %d/%d): %v",
				operation, attempt+1, retryLimit+1, err)
			if attempt < retryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}

		// Non-connection error or final attempt
		config.Error(pool.logger, "%s operation failed (attempt %d/%d): %v",
			operation, attempt+1, retryLimit+1, err)
		return fmt.Errorf("%s failed: %w", operation, err)
	}

	return err
}

// isTokenExpiredError checks if an error is due to token expiration
func isTokenExpiredError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	tokenErrors := []string{
		"token has expired",
		"token expired",
		"invalid token",
		"unauthorized",
		"authentication failed",
	}

	for _, tokenErr := range tokenErrors {
		if strings.Contains(errStr, tokenErr) {
			return true
		}
	}

	return false
}

// isConnectionError determines if an error is related to connection issues
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Check gRPC status codes
	s, ok := status.FromError(err)
	if ok {
		// Common gRPC status codes for connection issues
		switch s.Code() {
		case 14: // Unavailable
			return true
		case 1: // Cancelled
			return true
		case 4: // DeadlineExceeded
			return true
		}
	}

	// Check error strings for common connection issues
	errStr := err.Error()
	connectionErrors := []string{
		"connection refused",
		"broken pipe",
		"connection reset",
		"transport is closing",
		"timeout",
		"deadline exceeded",
		"no connection",
		"EOF",
	}

	for _, cerr := range connectionErrors {
		if err.Error() == cerr || (len(errStr) >= len(cerr) && errStr[:len(cerr)] == cerr) {
			return true
		}
	}

	return false
}

// Helper function to convert various value types to bytes
func toBytes(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	case nil:
		return nil, ErrNilValue
	default:
		// Marshal other types to JSON
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal value to JSON: %w", err)
		}
		return jsonBytes, nil
	}
}

// ========================================
// EXISTING API - UNCHANGED FUNCTION NAMES
// ========================================

func WithDatabase(dbName string) ImmuClientOption {
	return func(ic *config.ImmuClient) {
		ic.Database = dbName
	}
}

// ImmuClientOption defines functional options for ImmuClient configuration
type ImmuClientOption func(*config.ImmuClient)

// WithLogger sets a custom logger for the ImmuClient
func WithLogger(logger *config.AsyncLogger) ImmuClientOption {
	return func(ic *config.ImmuClient) {
		ic.Logger = logger
	}
}

// WithRetryLimit sets the maximum number of retry attempts
func WithRetryLimit(limit int) ImmuClientOption {
	return func(ic *config.ImmuClient) {
		ic.RetryLimit = limit
	}
}

// New creates and returns a connected ImmuClient (UNCHANGED - but now with connection pooling behind the scenes)
func New(options ...ImmuClientOption) (*config.ImmuClient, error) {
	// Create a default async logger
	defaultLogger, err := NewAsyncLogger()
	if err != nil {
		return nil, fmt.Errorf("failed to create default logger: %w", err)
	}

	// Create a default client
	ic := &config.ImmuClient{
		BaseCtx:     context.Background(),
		RetryLimit:  3,
		Logger:      defaultLogger,
		IsConnected: false,
	}

	// Apply custom options
	for _, option := range options {
		option(ic)
	}

	// Establish connection using the traditional approach
	// This maintains backward compatibility
	err = connect(ic)
	if err != nil {
		config.Close(ic.Logger)
		return nil, err
	}

	return ic, nil
}

// connect establishes a connection to ImmuDB (UNCHANGED)
func connect(ic *config.ImmuClient) error {
	// If database name is not set, use default
	if ic.Database == "" {
		ic.Database = config.DBName
	}

	config.Info(ic.Logger, "Connecting to ImmuDB at %s:%d for database %s",
		config.DBAddress, config.DBPort, ic.Database)

	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ic.BaseCtx, config.RequestTimeout)

	// Login to immudb with default credentials
	config.Info(ic.Logger, "Authenticating with ImmuDB")
	lr, err := c.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("login failed: %w", err)
	}

	// Store token for reconnection if needed
	ic.Token = lr.Token

	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Select database
	config.Info(ic.Logger, "Selecting database: %s", ic.Database)
	dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: ic.Database})
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("failed to use database %s: %w", ic.Database, err)
	}

	// Update token with database-specific token
	ic.Token = dbResp.Token

	// Create new context with the updated token
	md = metadata.Pairs("authorization", ic.Token)
	ctx = metadata.NewOutgoingContext(ic.BaseCtx, md)

	ic.Client = c
	ic.Ctx = ctx
	ic.Cancel = cancel
	ic.IsConnected = true
	config.Info(ic.Logger, "Successfully connected to ImmuDB database: %s", ic.Database)

	return nil
}

// reconnect attempts to reestablish a lost connection (UNCHANGED)
func reconnect(ic *config.ImmuClient) error {
	config.Warning(ic.Logger, "Attempting to reconnect to ImmuDB")

	// Clean up existing connection if any
	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		ic.Client.Disconnect()
	}

	ic.IsConnected = false

	// Attempt to connect again
	return connect(ic)
}

// withRetry executes the given operation with retry logic (UNCHANGED)
func withRetry(ic *config.ImmuClient, operation string, fn func() error) error {
	var err error

	for attempt := 0; attempt <= ic.RetryLimit; attempt++ {
		// Check connection status first
		if !ic.IsConnected {
			ic.Logger.Logger.Warn("Connection lost, attempting to reconnect before %s operation",
				zap.String("Operation", operation),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.withRetry"),
			)
			if err = reconnect(ic); err != nil {
				ic.Logger.Logger.Error("Reconnection attempt %d/%d failed: %v",
					zap.Int("Attempt", attempt+1),
					zap.Int("RetryLimit", ic.RetryLimit+1),
					zap.Error(err),
					zap.Time(logging.Created_at, time.Now()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, LOKI_URL),
					zap.String(logging.Function, "DB_OPs.withRetry"),
				)
				if attempt == ic.RetryLimit {
					return fmt.Errorf("%w: %v", ErrConnectionLost, err)
				}
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}

		// Execute the operation
		err = fn()

		// If successful, return nil
		if err == nil {
			return nil
		}

		// Check if error is due to connection issues
		if isConnectionError(err) {
			ic.Logger.Logger.Warn("%s operation failed due to connection issue (attempt %d/%d): %v",
				zap.String("Operation", operation),
				zap.Int("Attempt", attempt+1),
				zap.Int("RetryLimit", ic.RetryLimit+1),
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.withRetry"),
			)
			ic.IsConnected = false // Force reconnect on next attempt
			if attempt < ic.RetryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}

		// Non-connection error or final attempt
		ic.Logger.Logger.Error("%s operation failed (attempt %d/%d): %v",
			zap.String("Operation", operation),
			zap.Int("Attempt", attempt+1),
			zap.Int("RetryLimit", ic.RetryLimit+1),
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.withRetry"),
		)
		return fmt.Errorf("%s failed: %w", operation, err)
	}

	return err
}

// Create stores a value with the given key (UNCHANGED - but can optionally use connection pool)
func Create(ic *config.ImmuClient, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}

	if value == nil {
		return ErrNilValue
	}

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		return withPooledRetry("Create", func(conn *PooledConnection) error {
			// Convert value to bytes
			valueBytes, err := toBytes(value)
			if err != nil {
				return err
			}

			pool := GetGlobalPool()
			config.Info(pool.logger, "Creating key: %s", key)
			// Store the key-value pair
			_, err = conn.Client.Set(conn.Ctx, []byte(key), valueBytes)
			if err != nil {
				return err
			}

			config.Info(pool.logger, "Successfully created key: %s", key)
			return nil
		})
	}

	// Traditional approach with single connection
	return withRetry(ic, "Create", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		config.Info(ic.Logger, "Creating key: %s", key)
		// Store the key-value pair
		_, err = ic.Client.Set(ic.Ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}

		config.Info(ic.Logger, "Successfully created key: %s", key)
		return nil
	})
}

// Read retrieves a value by key (UNCHANGED - but can optionally use connection pool)
func Read(ic *config.ImmuClient, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("Read", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Reading key: %s", key)
			entry, err := conn.Client.Get(conn.Ctx, []byte(key))
			if err != nil {
				if strings.Contains(err.Error(), "key not found") ||
					strings.Contains(err.Error(), "tbtree: key not found") {
					return ErrNotFound
				}
				return err
			}

			entryValue = entry.Value
			return nil
		})

		if err != nil {
			return nil, err
		}

		return entryValue, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "Read", func() error {
		config.Info(ic.Logger, "Reading key: %s", key)
		entry, err := ic.Client.Get(ic.Ctx, []byte(key))
		if err != nil {
			if strings.Contains(err.Error(), "key not found") ||
				strings.Contains(err.Error(), "tbtree: key not found") {
				return ErrNotFound
			}
			return err
		}

		entryValue = entry.Value
		return nil
	})

	if err != nil {
		return nil, err
	}

	return entryValue, nil
}

// ReadJSON retrieves a value by key and unmarshals it into dest (UNCHANGED)
func ReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	data, err := Read(ic, key)
	if err != nil {
		return err
	}

	var logger *config.AsyncLogger
	if ic != nil {
		logger = ic.Logger
	} else {
		logger = GetGlobalPool().logger
	}

	config.Info(logger, "Unmarshaling JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value (UNCHANGED)
func Update(ic *config.ImmuClient, key string, value interface{}) error {
	// In ImmuDB, update is the same as create since it's an immutable database
	// We simply write the new value with the same key
	var logger *config.AsyncLogger
	if ic != nil {
		logger = ic.Logger
	} else {
		logger = GetGlobalPool().logger
	}
	config.Info(logger, "Updating key: %s", key)
	return Create(ic, key, value)
}

// GetKeys retrieves keys with a specified prefix (UNCHANGED - but can optionally use connection pool)
func GetKeys(ic *config.ImmuClient, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var keys []string

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("GetKeys", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Scanning keys with prefix: %s (limit: %d)", prefix, limit)
			scanReq := &schema.ScanRequest{
				Prefix: []byte(prefix),
				Limit:  uint64(limit),
			}

			scanResult, err := conn.Client.Scan(conn.Ctx, scanReq)
			if err != nil {
				return err
			}

			keys = make([]string, len(scanResult.Entries))
			for i, entry := range scanResult.Entries {
				keys[i] = string(entry.Key)
			}

			config.Info(pool.logger, "Found %d keys with prefix: %s", len(keys), prefix)
			return nil
		})

		if err != nil {
			return nil, err
		}

		return keys, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "GetKeys", func() error {
		config.Info(ic.Logger, "Scanning keys with prefix: %s (limit: %d)", prefix, limit)
		scanReq := &schema.ScanRequest{
			Prefix: []byte(prefix),
			Limit:  uint64(limit),
		}

		scanResult, err := ic.Client.Scan(ic.Ctx, scanReq)
		if err != nil {
			return err
		}

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}

		config.Info(ic.Logger, "Found %d keys with prefix: %s", len(keys), prefix)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return keys, nil
}

func GetAllKeys(ic *config.ImmuClient, prefix string) ([]string, error) {
	var allKeys []string
	batchSize := 1000
	var lastKey []byte

	for {
		// Create a batch request
		keys, err := getKeysBatch(ic, prefix, batchSize, lastKey)
		if err != nil {
			return nil, err
		}

		// If no keys returned, we're done
		if len(keys) == 0 {
			break
		}

		// Add keys to our result
		allKeys = append(allKeys, keys...)

		// If we got fewer than batch size, we're done
		if len(keys) < batchSize {
			break
		}

		// Set last key for next iteration
		lastKey = []byte(keys[len(keys)-1])
	}
	// Debugging output with a newline for clarity
	fmt.Printf("Total keys found: %d with Prefix: %s\n", len(allKeys), prefix)
	return allKeys, nil
}

// CountAllKeys counts all keys with a given prefix.
// This is more efficient than GetAllKeys as it doesn't store the keys.
func CountAllKeys(ic *config.ImmuClient, prefix string) (int, error) {
	var totalKeys int
	batchSize := 1000
	var lastKey []byte

	for {
		var count int
		err := withRetry(ic, "CountAllKeysBatch", func() error {
			scanReq := &schema.ScanRequest{
				Prefix:  []byte(prefix),
				Limit:   uint64(batchSize),
				SeekKey: lastKey,
				Desc:    false,
			}

			scanResult, err := ic.Client.Scan(ic.Ctx, scanReq)
			if err != nil {
				return err
			}

			count = len(scanResult.Entries)
			if count > 0 {
				lastKey = scanResult.Entries[count-1].Key
			}
			return nil
		})

		if err != nil {
			return 0, fmt.Errorf("failed to scan for keys count: %w", err)
		}

		totalKeys += count

		if count < batchSize {
			break // Reached the end of the keys.
		}
	}

	if ic != nil {
		config.Info(ic.Logger, "Total keys found: %d with Prefix: %s", totalKeys, prefix)
	}

	return totalKeys, nil
}

// CountTransactions counts the total number of transactions in the database.
func CountTransactions(mainDBClient *config.ImmuClient) (int, error) {
	// This function will scan for keys with the "tx:" prefix and count them.
	// It's more efficient than fetching all keys.
	return CountAllKeys(mainDBClient, "tx:")
}

// Helper function to get a batch of keys (UNCHANGED - but can optionally use connection pool)
func getKeysBatch(ic *config.ImmuClient, prefix string, limit int, seekKey []byte) ([]string, error) {
	var keys []string

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("GetKeysBatch", func(conn *PooledConnection) error {
			scanReq := &schema.ScanRequest{
				Prefix:  []byte(prefix),
				Limit:   uint64(limit),
				SeekKey: seekKey,
			}

			scanResult, err := conn.Client.Scan(conn.Ctx, scanReq)
			if err != nil {
				return err
			}

			keys = make([]string, len(scanResult.Entries))
			for i, entry := range scanResult.Entries {
				keys[i] = string(entry.Key)
			}

			return nil
		})

		if err != nil {
			return nil, err
		}

		return keys, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "GetKeysBatch", func() error {
		scanReq := &schema.ScanRequest{
			Prefix:  []byte(prefix),
			Limit:   uint64(limit),
			SeekKey: seekKey,
		}

		scanResult, err := ic.Client.Scan(ic.Ctx, scanReq)
		if err != nil {
			return err
		}

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return keys, nil
}

// BatchCreate stores multiple key-value pairs in a single transaction (UNCHANGED - but can optionally use connection pool)
func BatchCreate(ic *config.ImmuClient, entries map[string]interface{}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		return withPooledRetry("BatchCreate", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Creating batch of %d entries", len(entries))
			ops := make([]*schema.Op, 0, len(entries))

			for key, value := range entries {
				if key == "" {
					return ErrEmptyKey
				}

				if value == nil {
					return ErrNilValue
				}

				valueBytes, err := toBytes(value)
				if err != nil {
					return fmt.Errorf("failed to prepare value for key %s: %w", key, err)
				}

				ops = append(ops, &schema.Op{
					Operation: &schema.Op_Kv{
						Kv: &schema.KeyValue{
							Key:   []byte(key),
							Value: valueBytes,
						},
					},
				})
			}

			// Execute all operations in a single transaction
			_, err := conn.Client.ExecAll(conn.Ctx, &schema.ExecAllRequest{
				Operations: ops,
			})

			if err != nil {
				return err
			}

			config.Info(pool.logger, "Successfully created batch of %d entries", len(entries))
			return nil
		})
	}

	// Traditional approach with single connection
	return withRetry(ic, "BatchCreate", func() error {
		config.Info(ic.Logger, "Creating batch of %d entries", len(entries))
		ops := make([]*schema.Op, 0, len(entries))

		for key, value := range entries {
			if key == "" {
				return ErrEmptyKey
			}

			if value == nil {
				return ErrNilValue
			}

			valueBytes, err := toBytes(value)
			if err != nil {
				return fmt.Errorf("failed to prepare value for key %s: %w", key, err)
			}

			ops = append(ops, &schema.Op{
				Operation: &schema.Op_Kv{
					Kv: &schema.KeyValue{
						Key:   []byte(key),
						Value: valueBytes,
					},
				},
			})
		}

		// Execute all operations in a single transaction
		_, err := ic.Client.ExecAll(ic.Ctx, &schema.ExecAllRequest{
			Operations: ops,
		})

		if err != nil {
			return err
		}

		config.Info(ic.Logger, "Successfully created batch of %d entries", len(entries))
		return nil
	})
}

// Close closes the ImmuDB client connection (UNCHANGED)
func Close(ic *config.ImmuClient) error {
	config.Info(ic.Logger, "Closing ImmuDB connection")

	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		err := ic.Client.Disconnect()
		if err != nil {
			config.Error(ic.Logger, "Error disconnecting from ImmuDB: %v", err)
			return fmt.Errorf("error disconnecting from ImmuDB: %w", err)
		}
	}

	ic.IsConnected = false
	config.Info(ic.Logger, "ImmuDB connection closed successfully")

	// Close the logger
	err := config.Close(ic.Logger)
	if err != nil {
		return fmt.Errorf("error closing logger: %w", err)
	}

	return nil
}

// GetMerkleRoot returns the current database Merkle root (UNCHANGED - but can optionally use connection pool)
func GetMerkleRoot(ic *config.ImmuClient) ([]byte, error) {
	var merkleRoot []byte

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("GetMerkleRoot", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Getting current Merkle root")
			// Get current state from server
			state, err := conn.Client.CurrentState(conn.Ctx)
			if err != nil {
				return err
			}

			// Extract the Merkle root (txHash)
			merkleRoot = state.TxHash

			config.Info(pool.logger, "Database state: TxId=%d, TxHash=%x", state.TxId, state.TxHash)
			return nil
		})

		if err != nil {
			return nil, err
		}

		return merkleRoot, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "GetMerkleRoot", func() error {
		config.Info(ic.Logger, "Getting current Merkle root")
		// Get current state from server
		state, err := ic.Client.CurrentState(ic.Ctx)
		if err != nil {
			return err
		}

		// Extract the Merkle root (txHash)
		merkleRoot = state.TxHash

		config.Info(ic.Logger, "Database state: TxId=%d, TxHash=%x", state.TxId, state.TxHash)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return merkleRoot, nil
}

// SafeCreate stores a value with the given key and verifies the operation (UNCHANGED - but can optionally use connection pool)
func SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	var err error
	var PooledConnection *config.PooledConnection

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// If Connection is nil, use the global connection pool
		PooledConnection, err = GetAccountsConnection()
		if err != nil {
			return err
		}
		ic = PooledConnection.Client
	}

	// Check for empty key and nil value
	if key == "" {
		ic.Logger.Logger.Error("Empty key provided",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
		return ErrEmptyKey
	}

	// Check for nil value
	if value == nil {
		ic.Logger.Logger.Error("Nil value provided",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
		return ErrNilValue
	}

	// Traditional approach with single connection
	return withRetry(ic, "SafeCreate", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		ic.Logger.Logger.Info("Creating verified key: %s", 
		zap.String("key", key),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
		// Store the key-value pair with verification
		verifiedTx, err := ic.Client.VerifiedSet(ic.Ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}

		ic.Logger.Logger.Info("Transaction verified: tx=%d, verified=%v",
			zap.Uint64("tx", verifiedTx.Id),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
			)
		return nil
	})
}

// SafeRead retrieves a value by key with cryptographic verification (UNCHANGED - but can optionally use connection pool)
func SafeRead(ic *config.ImmuClient, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("SafeRead", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Reading verified key: %s", key)
			entry, err := conn.Client.VerifiedGet(conn.Ctx, []byte(key))
			if err != nil {
				if strings.Contains(err.Error(), "key not found") ||
					strings.Contains(err.Error(), "tbtree: key not found") {
					return ErrNotFound
				}
				return err
			}

			config.Info(pool.logger, "Value verified: tx=%d", entry.Tx)

			entryValue = entry.Value
			return nil
		})

		if err != nil {
			return nil, err
		}

		return entryValue, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "SafeRead", func() error {
		config.Info(ic.Logger, "Reading verified key: %s", key)
		entry, err := ic.Client.VerifiedGet(ic.Ctx, []byte(key))
		if err != nil {
			if strings.Contains(err.Error(), "key not found") ||
				strings.Contains(err.Error(), "tbtree: key not found") {
				return ErrNotFound
			}
			return err
		}

		config.Info(ic.Logger, "Value verified: tx=%d, verified=%v",
			entry.Tx, entry)

		entryValue = entry.Value
		return nil
	})

	if err != nil {
		return nil, err
	}

	return entryValue, nil
}

// SafeReadJSON retrieves a verified value by key and unmarshals it into dest (UNCHANGED)
func SafeReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	data, err := SafeRead(ic, key)
	if err != nil {
		return err
	}

	var logger *config.AsyncLogger
	if ic != nil {
		logger = ic.Logger
	} else {
		logger = GetGlobalPool().logger
	}

	config.Info(logger, "Unmarshaling verified JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// GetHistory retrieves the history of values for a key (UNCHANGED - but can optionally use connection pool)
func GetHistory(ic *config.ImmuClient, key string, limit int) ([]*schema.Entry, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var entries []*schema.Entry

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("GetHistory", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Getting history for key: %s (limit: %d)", key, limit)
			historyReq := &schema.HistoryRequest{
				Key:   []byte(key),
				Limit: int32(limit),
			}

			historyResp, err := conn.Client.History(conn.Ctx, historyReq)
			if err != nil {
				return err
			}

			entries = historyResp.Entries
			config.Info(pool.logger, "Found %d historical entries for key: %s", len(entries), key)
			return nil
		})

		if err != nil {
			return nil, err
		}

		return entries, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "GetHistory", func() error {
		config.Info(ic.Logger, "Getting history for key: %s (limit: %d)", key, limit)
		historyReq := &schema.HistoryRequest{
			Key:   []byte(key),
			Limit: int32(limit),
		}

		historyResp, err := ic.Client.History(ic.Ctx, historyReq)
		if err != nil {
			return err
		}

		entries = historyResp.Entries
		config.Info(ic.Logger, "Found %d historical entries for key: %s", len(entries), key)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return entries, nil
}

// NewBlockHasher creates a new BlockHasher (UNCHANGED)
func NewBlockHasher() *config.BlockHasher {
	return &config.BlockHasher{}
}

// HashBlock generates a hash for a block using the nonce, sender, and timestamp (UNCHANGED)
func HashBlock(h *config.BlockHasher, nonce, sender string, timestamp int64) string {
	// Create a deterministic string from the block components
	data := fmt.Sprintf("%s-%s-%d", nonce, sender, timestamp)

	// Hash the data
	hash := sha256.Sum256([]byte(data))

	// Return hex-encoded hash, truncated for readability
	return hex.EncodeToString(hash[:])[:16]
}

// GetDatabaseState returns the current state of the database (UNCHANGED - but can optionally use connection pool)
func GetDatabaseState(ic *config.ImmuClient) (*schema.ImmutableState, error) {
	var state *schema.ImmutableState

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		err := withPooledRetry("GetDatabaseState", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Getting current database state")
			// Get current state from server
			dbState, err := conn.Client.CurrentState(conn.Ctx)
			if err != nil {
				// Check if this is a token expired error
				if strings.Contains(err.Error(), "token has expired") {
					// Token refresh will be handled by the pool automatically
					return err
				} else {
					return err
				}
			}

			state = dbState
			config.Info(pool.logger, "Database state retrieved: TxId=%d", state.TxId)
			return nil
		})

		if err != nil {
			return nil, err
		}

		return state, nil
	}

	// Traditional approach with single connection
	err := withRetry(ic, "GetDatabaseState", func() error {
		config.Info(ic.Logger, "Getting current database state")
		// Get current state from server
		dbState, err := ic.Client.CurrentState(ic.Ctx)
		if err != nil {
			// Check if this is a token expired error
			if strings.Contains(err.Error(), "token has expired") {
				// Re-authenticate to get a new token
				if reconnErr := reconnect(ic); reconnErr != nil {
					return fmt.Errorf("failed to reconnect after token expiration: %w", reconnErr)
				}
				// Try again with new token
				dbState, err = ic.Client.CurrentState(ic.Ctx)
				if err != nil {
					return err
				}
			} else {
				return err
			}
		}

		state = dbState
		config.Info(ic.Logger, "Database state retrieved: TxId=%d", state.TxId)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return state, nil
}

// Exists checks if a key exists in the database (UNCHANGED)
func Exists(ic *config.ImmuClient, key string) (bool, error) {
	if key == "" {
		return false, ErrEmptyKey
	}

	_, err := Read(ic, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// Transaction allows execution of multiple operations in one transaction (UNCHANGED)
func Transaction(ic *config.ImmuClient, fn func(tx *config.ImmuTransaction) error) error {
	// Create transaction object
	tx := &config.ImmuTransaction{
		Client: ic,
		Ops:    make([]*schema.Op, 0),
	}

	// Execute transaction function
	if err := fn(tx); err != nil {
		return err
	}

	// If no operations, just return
	if len(tx.Ops) == 0 {
		return nil
	}

	// Execute transaction
	return withRetry(ic, "Transaction", func() error {
		config.Info(ic.Logger, "Executing transaction with %d operations", len(tx.Ops))
		_, err := ic.Client.ExecAll(ic.Ctx, &schema.ExecAllRequest{
			Operations: tx.Ops,
		})

		if err != nil {
			return err
		}

		config.Info(ic.Logger, "Transaction with %d operations executed successfully", len(tx.Ops))
		return nil
	})
}

// Set adds a set operation to the transaction (UNCHANGED)
func Set(tx *config.ImmuTransaction, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}

	if value == nil {
		return ErrNilValue
	}

	valueBytes, err := toBytes(value)
	if err != nil {
		return fmt.Errorf("failed to prepare value for key %s: %w", key, err)
	}

	tx.Ops = append(tx.Ops, &schema.Op{
		Operation: &schema.Op_Kv{
			Kv: &schema.KeyValue{
				Key:   []byte(key),
				Value: valueBytes,
			},
		},
	})

	return nil
}

// IsHealthy checks if the database connection is healthy (UNCHANGED)
func IsHealthy(ic *config.ImmuClient) bool {
	if ic == nil {
		// Check pool health
		pool := GetGlobalPool()
		stats := pool.GetPoolStats()
		return !stats["pool_closed"].(bool) && stats["total_connections"].(int) > 0
	}

	if !ic.IsConnected {
		return false
	}

	// Try to get current state as a health check
	_, err := ic.Client.CurrentState(ic.Ctx)
	return err == nil
}

// Ping performs a health check on the database (UNCHANGED - but can optionally use connection pool)
func Ping(ic *config.ImmuClient) error {
	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use connection pool approach
		return withPooledRetry("Ping", func(conn *PooledConnection) error {
			pool := GetGlobalPool()
			config.Info(pool.logger, "Pinging ImmuDB")
			_, err := conn.Client.CurrentState(conn.Ctx)
			if err != nil {
				return err
			}
			config.Info(pool.logger, "ImmuDB ping successful")
			return nil
		})
	}

	// Traditional approach with single connection
	return withRetry(ic, "Ping", func() error {
		config.Info(ic.Logger, "Pinging ImmuDB")
		_, err := ic.Client.CurrentState(ic.Ctx)
		if err != nil {
			return err
		}
		config.Info(ic.Logger, "ImmuDB ping successful")
		return nil
	})
}

// StoreZKBlock stores a complete ZK block in the main database (UNCHANGED)
func StoreZKBlock(mainDBClient *config.ImmuClient, block *config.ZKBlock) error {
	// Create a unique key for the block
	blockKey := fmt.Sprintf("block:%d", block.BlockNumber)

	// Store the full block data
	if err := SafeCreate(mainDBClient, blockKey, block); err != nil {
		return fmt.Errorf("failed to store block %d: %w", block.BlockNumber, err)
	}

	// Also store by hash for lookups
	hashKey := fmt.Sprintf("block:hash:%s", block.BlockHash.Hex())
	if err := Create(mainDBClient, hashKey, blockKey); err != nil {
		return fmt.Errorf("failed to store block hash mapping: %w", err)
	}

	// Store the latest block number for quick access
	if err := Create(mainDBClient, "latest_block", block.BlockNumber); err != nil {
		return fmt.Errorf("failed to update latest block: %w", err)
	}

	// Store each transaction hash -> block number mapping for lookups
	for _, tx := range block.Transactions {
		txKey := fmt.Sprintf("tx:%s", tx.Hash)
		if err := Create(mainDBClient, txKey, block.BlockNumber); err != nil {
			return fmt.Errorf("failed to store tx mapping for %s: %w", tx.Hash, err)
		}
	}

	var logger *config.AsyncLogger
	if mainDBClient != nil {
		logger = mainDBClient.Logger
	} else {
		logger = GetGlobalPool().logger
	}

	config.Info(logger, "Successfully stored block %d with hash %s and %d transactions",
		block.BlockNumber, block.BlockHash.Hex(), len(block.Transactions))

	return nil
}

// GetZKBlockByNumber retrieves a ZK block by its number (UNCHANGED)
func GetZKBlockByNumber(mainDBClient *config.ImmuClient, blockNumber uint64) (*config.ZKBlock, error) {
	blockKey := fmt.Sprintf("block:%d", blockNumber)

	block := new(config.ZKBlock)
	if err := SafeReadJSON(mainDBClient, blockKey, block); err != nil {
		return nil, fmt.Errorf("failed to retrieve block %d: %w", blockNumber, err)
	}

	return block, nil
}

// GetZKBlockByHash retrieves a ZK block by its hash (UNCHANGED)
func GetZKBlockByHash(mainDBClient *config.ImmuClient, blockHash string) (*config.ZKBlock, error) {
	// First get the block number from the hash
	hashKey := fmt.Sprintf("block:hash:%s", blockHash)

	blockKeyBytes, err := Read(mainDBClient, hashKey)
	if err != nil {
		return nil, fmt.Errorf("failed to find block with hash %s: %w", blockHash, err)
	}

	// Then get the block using the block key
	blockKey := string(blockKeyBytes)

	block := new(config.ZKBlock)
	if err := SafeReadJSON(mainDBClient, blockKey, block); err != nil {
		return nil, fmt.Errorf("failed to retrieve block by hash %s: %w", blockHash, err)
	}

	return block, nil
}

// GetLatestBlockNumber returns the latest block number (UNCHANGED)
func GetLatestBlockNumber(mainDBClient *config.ImmuClient) (uint64, error) {
	latestBytes, err := Read(mainDBClient, "latest_block")
	if err != nil {
		// Check for both our custom ErrNotFound and the ImmuDB-specific errors
		if err == ErrNotFound ||
			strings.Contains(err.Error(), "key not found") ||
			strings.Contains(err.Error(), "tbtree: key not found") {
			var logger *config.AsyncLogger
			if mainDBClient != nil {
				logger = mainDBClient.Logger
			} else {
				logger = GetGlobalPool().logger
			}
			config.Info(logger, "No blocks found in the database yet")
			return 0, nil // No blocks yet
		}
		return 0, fmt.Errorf("failed to get latest block: %w", err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(latestBytes, &blockNumber); err != nil {
		return 0, fmt.Errorf("failed to parse latest block number: %w", err)
	}

	return blockNumber, nil
}

// GetTransactionBlock returns the block containing a specific transaction (UNCHANGED)
func GetTransactionBlock(mainDBClient *config.ImmuClient, txHash string) (*config.ZKBlock, error) {
	txKey := fmt.Sprintf("tx:%s", txHash)

	blockNumberBytes, err := Read(mainDBClient, txKey)
	if err != nil {
		return nil, fmt.Errorf("transaction %s not found: %w", txHash, err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(blockNumberBytes, &blockNumber); err != nil {
		return nil, fmt.Errorf("failed to parse block number for tx %s: %w", txHash, err)
	}

	return GetZKBlockByNumber(mainDBClient, blockNumber)
}

// Get Transaction by hash
func GetTransactionByHash(mainDBClient *config.ImmuClient, txHash string) (*config.ZKBlockTransaction, error) {
	// Get the block that contains the transaction.
	block, err := GetTransactionBlock(mainDBClient, txHash)
	if err != nil {
		return nil, err
	}

	// Find the transaction in the block.
	var zkTx *config.ZKBlockTransaction
	for i := range block.Transactions {
		if block.Transactions[i].Hash == txHash {
			zkTx = &block.Transactions[i]
			break
		}
	}

	if zkTx == nil {
		return nil, fmt.Errorf("transaction %s not found in block %d", txHash, block.BlockNumber)
	}

	return zkTx, nil
}

// GetTransactionsBatch fetches multiple transactions by their hashes in a single batch
func GetTransactionsBatch(mainDBClient *config.ImmuClient, hashes []string) ([]*config.ZKBlockTransaction, error) {
    var transactions []*config.ZKBlockTransaction
    
    // Process in batches to avoid too many concurrent requests
    batchSize := 10
    for i := 0; i < len(hashes); i += batchSize {
        end := i + batchSize
        if end > len(hashes) {
            end = len(hashes)
        }
        
        batch := hashes[i:end]
        var wg sync.WaitGroup
        var mu sync.Mutex
        var batchErr error
        
        for _, hash := range batch {
            wg.Add(1)
            go func(h string) {
                defer wg.Done()
                
                tx, err := GetTransactionByHash(mainDBClient, h)
                if err != nil {
                    batchErr = fmt.Errorf("failed to fetch transaction %s: %w", h, err)
                    return
                }
                
                mu.Lock()
                transactions = append(transactions, tx)
                mu.Unlock()
            }(hash)
        }
        
        wg.Wait()
        if batchErr != nil {
            return nil, batchErr
        }
    }
    
    return transactions, nil
}

func GetAllBlocks(mainDBClient *config.ImmuClient) ([]*config.ZKBlock, error) {
	latestBlockNumber, err := GetLatestBlockNumber(mainDBClient)
	if err != nil {
		return nil, err
	}
	var blocks []*config.ZKBlock
	for i := latestBlockNumber; i >= 1; i-- {
		block, err := GetZKBlockByNumber(mainDBClient, i)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// ========================================
// OPTIONAL ENHANCED FUNCTIONS FOR CONVENIENCE
// ========================================

// EnableConnectionPooling enables connection pooling with custom configuration
func EnableConnectionPooling(poolConfig *ConnectionPoolConfig) error {
	return InitializeGlobalPool(poolConfig)
}

// GetPoolStatistics returns connection pool statistics (new convenience function)
func GetPoolStatistics() map[string]interface{} {
	return GetGlobalPool().GetPoolStats()
}

// CloseGlobalPool closes the global connection pool (new convenience function)
func CloseGlobalPool() error {
	poolMutex.Lock()
	defer poolMutex.Unlock()

	if globalPool != nil {
		err := globalPool.Close()
		globalPool = nil
		return err
	}

	return nil
}
