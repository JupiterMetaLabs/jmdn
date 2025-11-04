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
	"github.com/ethereum/go-ethereum/common"
	"go.uber.org/zap"
	"google.golang.org/grpc/status"
)

const (
	DEFAULT_PREFIX_TX      = "tx:"
	PREFIX_BLOCK           = "block:"
	PREFIX_BLOCK_HASH      = "block:hash:"
	DEFAULT_PREFIX_RECEIPT = "receipt:"
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

// reconnect attempts to reestablish a lost connection (UNCHANGED)
func reconnect(ic *config.ImmuClient, FUNCTION string) error {
	// Debugging
	// fmt.Println("reconnect called, Function Name that called reconnect is ", FUNCTION)
	ic.Logger.Logger.Warn("Connection lost, attempting to reconnect before operation",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.reconnect"),
	)
	// Clean up existing connection if any
	if ic.Cancel != nil {
		ic.Logger.Logger.Warn("Canceling existing connection",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.reconnect"),
		)
		ic.Cancel()
	}

	if ic.Client != nil {
		ic.Client.Disconnect()
	}

	ic.IsConnected = false

	// Don't get a new connection from the pool - this causes pool exhaustion
	// Instead, mark as disconnected and let the calling code handle it
	ic.Logger.Logger.Warn("Connection marked as disconnected - calling code should handle reconnection",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.reconnect"),
	)

	// Return error to indicate reconnection failed
	return fmt.Errorf("connection lost - cannot reconnect without getting new connection from pool")
}

// withRetry executes the given operation with retry logic (UNCHANGED)
func withRetry(ic *config.ImmuClient, operation string, fn func() error) error {
	var err error

	for attempt := 0; attempt <= ic.RetryLimit; attempt++ {
		// Debugging
		// fmt.Println("withRetry called, Function Name that called withRetry is ", operation)
		// Check connection status first
		if !ic.IsConnected {
			ic.Logger.Logger.Warn("Connection lost, attempting to reconnect before %s operation",
				zap.String("Operation", operation),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.withRetry"),
			)
			if err = reconnect(ic, operation); err != nil {
				ic.Logger.Logger.Error("Reconnection attempt %d/%d failed: %v",
					zap.Int("Attempt", attempt+1),
					zap.Int("RetryLimit", ic.RetryLimit+1),
					zap.Error(err),
					zap.Time(logging.Created_at, time.Now().UTC()),
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
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.withRetry"),
			)
			ic.IsConnected = false // Mark as disconnected

			// Try to reconnect, but don't retry if reconnection fails
			if reconnectErr := reconnect(ic, "withRetry"); reconnectErr != nil {
				ic.Logger.Logger.Error("Reconnection failed during retry - cannot reconnect without getting new connection from pool: %v",
					zap.Error(reconnectErr),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Loki_url, LOKI_URL),
					zap.String(logging.Function, "DB_OPs.withRetry"),
				)
				return fmt.Errorf("connection lost - cannot reconnect without getting new connection from pool: %w", reconnectErr)
			}

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
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.withRetry"),
		)
		return fmt.Errorf("%s failed: %w", operation, err)
	}

	return err
}

// Create stores a value with the given key using the connection pool
func Create(PooledConnection *config.PooledConnection, key string, value interface{}) error {
	var ic *config.ImmuClient
	var shouldReturnConnection bool = false
	if key == "" {
		return ErrEmptyKey
	}

	if value == nil {
		return ErrNilValue
	}

	// Get a connection from the pool
	if PooledConnection == nil {
		var err error
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Create"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.Create"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic = PooledConnection.Client
	// Ensure the database is selected
	if err := ensureMainDBSelected(PooledConnection); err != nil {
		ic.Logger.Logger.Error("Failed to ensure main database selected",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Create"),
		)
		return fmt.Errorf("database selection failed: %w", err)
	}

	// Convert value to bytes
	valueBytes, err := toBytes(value)
	if err != nil {
		ic.Logger.Logger.Error("Failed to convert value to bytes",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Create"),
		)
		return fmt.Errorf("failed to convert value to bytes: %w", err)
	}

	// Log the operation
	ic.Logger.Logger.Info("Creating key",
		zap.String("key", key),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.Create"),
	)

	// Store the key-value pair
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = ic.Client.Set(ctx, []byte(key), valueBytes)
	if err != nil {
		ic.Logger.Logger.Error("Failed to set key",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Create"),
		)
		return fmt.Errorf("failed to set key %s: %w", key, err)
	}

	// Log success
	ic.Logger.Logger.Info("Successfully created key",
		zap.String("key", key),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.Create"),
	)

	return nil
}

// Read retrieves a value by key using the connection pool
func Read(PooledConnection *config.PooledConnection, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	var err error
	var shouldReturnConnection bool = false

	// Handle nil or invalid connection
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Read"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.Read"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Ensure the database is selected
	if err := ensureMainDBSelected(PooledConnection); err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to ensure main database selected",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Read"),
		)
		return nil, fmt.Errorf("database selection failed: %w", err)
	}

	// Log the read operation
	PooledConnection.Client.Logger.Logger.Info("Reading key from database",
		zap.String("key", key),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.Read"),
	)

	// Execute the read operation
	// Create a fresh context for the read operation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entry, err := PooledConnection.Client.Client.Get(ctx, []byte(key))
	if err != nil {
		if isNotFoundError(err) {
			PooledConnection.Client.Logger.Logger.Warn("Key not found",
				zap.String("key", key),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "DB_OPs.Read"),
			)
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("database read failed: %w", err)
	}

	entryValue = entry.Value

	// Log successful read
	PooledConnection.Client.Logger.Logger.Info("Successfully read key",
		zap.String("key", key),
		zap.Int("value_length", len(entryValue)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.Read"),
	)

	return entryValue, nil
}

// Helper function to check if error is a "not found" error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "key not found") ||
		strings.Contains(err.Error(), "tbtree: key not found")
}

// ReadJSON retrieves a value by key and unmarshals it into dest (UNCHANGED)
func ReadJSON(PooledConnection *config.PooledConnection, key string, dest interface{}) error {
	var err error
	var data []byte
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ReadJSON"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.ReadJSON"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	data, err = Read(PooledConnection, key)
	if err != nil {
		return err
	}

	PooledConnection.Client.Logger.Logger.Info("Unmarshaling JSON data for key: %s",
		zap.String("key", key),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ReadJSON"),
	)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value (UNCHANGED)
func Update(PooledConnection *config.PooledConnection, key string, value interface{}) error {
	var err error
	var shouldReturnConnection bool = false
	// In ImmuDB, update is the same as create since it's an immutable database
	// We simply write the new value with the same key
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Update"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.Update"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}
	return Create(PooledConnection, key, value)
}

// GetKeys retrieves keys with a specified prefix (UNCHANGED - but can optionally use connection pool)
func GetKeys(PooledConnection *config.PooledConnection, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var keys []string
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetKeys"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetKeys"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if PooledConnection != nil {
		// Use connection pool approach
		PooledConnection.Client.Logger.Logger.Info("Scanning keys with prefix: %s (limit: %d)",
			zap.String("prefix", prefix),
			zap.Int("limit", limit),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetKeys"),
		)
		scanReq := &schema.ScanRequest{
			Prefix: []byte(prefix),
			Limit:  uint64(limit),
		}

		// Create a fresh context for the scan operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		scanResult, err := PooledConnection.Client.Client.Scan(ctx, scanReq)
		if err != nil {
			PooledConnection.Client.Logger.Logger.Error("Failed to scan keys with prefix: %s (limit: %d)",
				zap.String("prefix", prefix),
				zap.Int("limit", limit),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetKeys"),
			)
			return nil, err
		}

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}

		PooledConnection.Client.Logger.Logger.Info("Found %d keys with prefix: %s",
			zap.Int("count", len(keys)),
			zap.String("prefix", prefix),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetKeys"),
		)
		return keys, nil
	}
	PooledConnection.Client.Logger.Logger.Info("Found %d keys with prefix: %s",
		zap.Int("count", len(keys)),
		zap.String("prefix", prefix),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetKeys"),
	)
	return keys, nil
}

func GetAllKeys(PooledConnection *config.PooledConnection, prefix string) ([]string, error) {
	var allKeys []string
	batchSize := 1000
	var lastKey []byte
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetAllKeys"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetAllKeys"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	batchNum := 0
	for {
		batchNum++
		// Create a batch request
		fmt.Printf(">>> [DB] Getting batch %d for prefix '%s' (current count: %d keys)...\n", batchNum, prefix, len(allKeys))
		rawKeys, err := getKeysBatch(PooledConnection, prefix, batchSize, lastKey)
		if err != nil {
			fmt.Printf(">>> [DB] ERROR: Failed to get batch %d for prefix '%s': %v\n", batchNum, prefix, err)
			PooledConnection.Client.Logger.Logger.Error("Failed to scan keys with prefix: %s (limit: %d)",
				zap.String("prefix", prefix),
				zap.Int("limit", batchSize),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetKeys"),
			)
			return nil, err
		}

		// Validate that all keys match the prefix (SeekKey might cause issues)
		validKeys := make([]string, 0, len(rawKeys))
		for _, key := range rawKeys {
			if strings.HasPrefix(key, prefix) {
				validKeys = append(validKeys, key)
			} else {
				// If we get a key that doesn't match the prefix, we've gone past the prefix range
				fmt.Printf(">>> [DB] WARNING: Key '%s' doesn't match prefix '%s' - stopping scan\n", key, prefix)
				break
			}
		}

		// Check for duplicates
		keysSet := make(map[string]bool)
		uniqueKeys := make([]string, 0, len(validKeys))
		for _, key := range validKeys {
			if !keysSet[key] {
				keysSet[key] = true
				uniqueKeys = append(uniqueKeys, key)
			} else {
				fmt.Printf(">>> [DB] WARNING: Duplicate key found: '%s'\n", key)
			}
		}

		fmt.Printf(">>> [DB] ✓ Got batch %d: %d raw keys for prefix '%s' (%d valid, %d unique, total so far: %d keys)\n",
			batchNum, len(rawKeys), prefix, len(validKeys), len(uniqueKeys), len(allKeys)+len(uniqueKeys))

		// If no keys returned, we're done
		if len(uniqueKeys) == 0 {
			fmt.Printf(">>> [DB] No more valid keys for prefix '%s', stopping\n", prefix)
			break
		}

		// Add keys to our result
		allKeys = append(allKeys, uniqueKeys...)

		// Use uniqueKeys for the rest of the logic
		keys := uniqueKeys

		// If we got fewer than batch size, we're done
		if len(keys) < batchSize {
			fmt.Printf(">>> [DB] Got fewer than batch size (%d < %d), stopping\n", len(keys), batchSize)
			break
		}

		// Set last key for next iteration
		lastKey = []byte(keys[len(keys)-1])
		fmt.Printf(">>> [DB] Continuing to next batch for prefix '%s'...\n", prefix)
	}
	// Debugging output with a newline for clarity
	fmt.Printf("Total keys found: %d with Prefix: %s\n", len(allKeys), prefix)
	return allKeys, nil
}

// CountAllKeys counts all keys with a given prefix.
// This is more efficient than GetAllKeys as it doesn't store the keys.
func CountAllKeys(PooledConnection *config.PooledConnection, prefix string) (int, error) {
	var totalKeys int
	batchSize := 1000
	var lastKey []byte
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return -1, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.CountAllKeys"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.CountAllKeys"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}
	for {
		var count int
		scanReq := &schema.ScanRequest{
			Prefix:  []byte(prefix),
			Limit:   uint64(batchSize),
			SeekKey: lastKey,
			Desc:    false,
		}

		// Create a fresh context for the scan operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		scanResult, err := PooledConnection.Client.Client.Scan(ctx, scanReq)
		if err != nil {
			return 0, fmt.Errorf("failed to scan for keys count: %w", err)
		}

		count = len(scanResult.Entries)
		if count > 0 {
			lastKey = scanResult.Entries[count-1].Key
		}

		totalKeys += count

		if count < batchSize {
			break // Reached the end of the keys.
		}
	}
	PooledConnection.Client.Logger.Logger.Info("Total keys found: %d with Prefix: %s",
		zap.Int("count", totalKeys),
		zap.String("prefix", prefix),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.CountAllKeys"),
	)
	return totalKeys, nil
}

// CountTransactionsByAccount counts the number of transactions for a specific account
func CountTransactionsByAccount(mainDBClient *config.PooledConnection, accountAddr *common.Address) (int64, error) {
	var err error
	var shouldReturnConnection bool = false

	if mainDBClient == nil || mainDBClient.Client == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return 0, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved for transaction counting",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.CountTransactionsByAccount"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection returned to pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.CountTransactionsByAccount"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	// Get all transactions for this account
	transactions, err := GetTransactionsByAccount(mainDBClient, accountAddr)
	if err != nil {
		return 0, fmt.Errorf("failed to get transactions for account: %w", err)
	}

	return int64(len(transactions)), nil
}

// CountTransactions counts the total number of transactions in the database.
func CountTransactions(mainDBClient *config.PooledConnection) (int, error) {
	// This function will scan for keys with the "tx:" prefix and count them.
	// It's more efficient than fetching all keys.
	return CountAllKeys(mainDBClient, DEFAULT_PREFIX_TX)
}

// Helper function to get a batch of keys (UNCHANGED - but can optionally use connection pool)
func getKeysBatch(PooledConnection *config.PooledConnection, prefix string, limit int, seekKey []byte) ([]string, error) {
	var keys []string
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.getKeysBatch"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.getKeysBatch"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}
	ic := PooledConnection.Client
	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic != nil {
		// Use connection pool approach
		scanReq := &schema.ScanRequest{
			Prefix:  []byte(prefix),
			Limit:   uint64(limit),
			SeekKey: seekKey,
		}

		ic.Logger.Logger.Info("Scanning keys with prefix: %s (limit: %d)",
			zap.String("prefix", prefix),
			zap.Int("limit", limit),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.getKeysBatch"),
		)

		fmt.Printf(">>> [DB] Executing Scan for prefix '%s' (limit: %d, seekKey: %v)...\n", prefix, limit, len(seekKey) > 0)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Increased timeout for large datasets
		defer cancel()

		scanResult, err := ic.Client.Scan(ctx, scanReq)
		if err != nil {
			fmt.Printf(">>> [DB] ERROR: Scan failed for prefix '%s': %v\n", prefix, err)
			ic.Logger.Logger.Error("Failed to scan keys with prefix: %s (limit: %d)",
				zap.String("prefix", prefix),
				zap.Int("limit", limit),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.getKeysBatch"),
			)
			return nil, err
		}
		fmt.Printf(">>> [DB] ✓ Scan completed for prefix '%s': %d entries returned\n", prefix, len(scanResult.Entries))
		ic.Logger.Logger.Info("Scanned keys with prefix: %s (limit: %d)",
			zap.String("prefix", prefix),
			zap.Int("limit", limit),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.getKeysBatch"),
		)

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}
		ic.Logger.Logger.Info("Keys scanned successfully",
			zap.String("prefix", prefix),
			zap.Int("limit", limit),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.getKeysBatch"),
		)
		return keys, nil
	}

	fmt.Println("Keys scanned Failed - Config.Immuclient Not Found")
	return nil, nil
}

// BatchCreate stores multiple key-value pairs in a single transaction
func BatchCreate(PooledConnection *config.PooledConnection, entries map[string]interface{}) error {
	if len(entries) == 0 {
		fmt.Println("BatchCreate Failed - Empty Batch")
		return ErrEmptyBatch
	}

	var err error
	var shouldReturnConnection bool = false

	// Handle nil or invalid connection
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			fmt.Println("BatchCreate Failed - Config.Immuclient Not Found")
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.BatchCreate"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "DB_OPs.BatchCreate"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Ensure the database for this pooled connection is selected
	if err := ensureConnectionDatabaseSelected(PooledConnection); err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to ensure database selected",
			zap.Error(err),
			zap.String(logging.Connection_database, PooledConnection.Database),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.BatchCreate"),
		)
		return fmt.Errorf("database selection failed: %w", err)
	}

	// Log the batch operation
	PooledConnection.Client.Logger.Logger.Info("Creating batch of entries",
		zap.Int("entry_count", len(entries)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.BatchCreate"),
	)

	// Prepare operations
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
			PooledConnection.Client.Logger.Logger.Error("Failed to prepare value for batch operation",
				zap.String("key", key),
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "DB_OPs.BatchCreate"),
			)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{
		Operations: ops,
	})

	if err != nil {
		PooledConnection.Client.Logger.Logger.Error("Batch create operation failed",
			zap.Error(err),
			zap.Int("entry_count", len(entries)),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.BatchCreate"),
		)
		return fmt.Errorf("batch operation failed: %w", err)
	}

	// Log success
	PooledConnection.Client.Logger.Logger.Info("Successfully created batch of entries",
		zap.Int("entry_count", len(entries)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.BatchCreate"),
	)

	return nil
}

// Close closes the ImmuDB client connection (UNCHANGED)
func Close(ic *config.ImmuClient) error {
	ic.Logger.Logger.Info("Closing ImmuDB connection",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.Close"),
	)

	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		err := ic.Client.Disconnect()
		if err != nil {
			ic.Logger.Logger.Error("Error disconnecting from ImmuDB: %v",
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "DB_OPs.Close"),
			)
			return fmt.Errorf("error disconnecting from ImmuDB: %w", err)
		}
	}

	ic.IsConnected = false
	ic.Logger.Logger.Info("ImmuDB connection closed successfully",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "DB_OPs.Close"),
	)

	// Close the logger - Close after Sync and dumping all the buffered logs
	// Close the logger - Close after Sync and dumping all the buffered logs
	defer func() {
		ic.Logger.Logger.Info("ImmuDB connection closed successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.Close"),
		)
		ic.Logger.Logger.Sync()
	}()

	return nil
}

// GetMerkleRoot returns the current database Merkle root
func GetMerkleRoot(PooledConnection *config.PooledConnection) ([]byte, error) {
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountsConnection()
		if err != nil {
			return nil, err
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetMerkleRoot"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetMerkleRoot"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Create a fresh context for the operation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	state, err := PooledConnection.Client.Client.CurrentState(ctx)
	if err != nil {
		PooledConnection.Client.Logger.Logger.Error("Failed to get current state",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetMerkleRoot"),
		)
		return nil, err
	}
	PooledConnection.Client.Logger.Logger.Info("Successfully retrieved current state",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetMerkleRoot"),
	)
	// Extract the Merkle root hash from the state
	return state.TxHash, nil
}

// SafeCreate stores a value with the given key and verifies the operation (UNCHANGED - but can optionally use connection pool)
func SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	fmt.Printf("=== DEBUG: SafeCreate called with key: %s ===\n", key)

	var err error
	var PooledConnection *config.PooledConnection
	var shouldReturnConnection bool = false

	// If ic is nil, we need to determine which database to use based on context
	// For now, we'll default to accounts database since this is called from UpdateAccountBalance
	if ic == nil {
		fmt.Println("DEBUG: ic is nil, getting accounts connection")
		PooledConnection, err = GetAccountsConnection()
		if err != nil {
			fmt.Printf("DEBUG: Failed to get accounts connection: %v\n", err)
			return err
		}
		shouldReturnConnection = true
		ic = PooledConnection.Client
		fmt.Println("DEBUG: Successfully got accounts connection")

		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
	} else {
		fmt.Println("DEBUG: Using provided ic client")
	}

	// Check for empty key and nil value
	if key == "" {
		ic.Logger.Logger.Error("Empty key provided",
			zap.Time(logging.Created_at, time.Now().UTC()),
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
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
		return ErrNilValue
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Client Connection is returned to the Pool",
				zap.String(logging.Connection_database, config.AccountsDBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.SafeCreate"),
			)
			PutAccountsConnection(PooledConnection)
		}()
	}

	// Traditional approach with single connection (no withRetry for pooled connections)
	// Convert value to bytes
	fmt.Println("DEBUG: Converting value to bytes")
	valueBytes, err := toBytes(value)
	if err != nil {
		fmt.Printf("DEBUG: Failed to convert value to bytes: %v\n", err)
		return err
	}
	fmt.Printf("DEBUG: Successfully converted value to bytes (length: %d)\n", len(valueBytes))

	ic.Logger.Logger.Info("Creating verified key",
		zap.String("key", key),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.SafeCreate"),
	)

	// Store the key-value pair with verification
	fmt.Println("DEBUG: Creating context and calling VerifiedSet")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	verifiedTx, err := ic.Client.VerifiedSet(ctx, []byte(key), valueBytes)
	if err != nil {
		fmt.Printf("DEBUG: VerifiedSet failed: %v\n", err)
		ic.Logger.Logger.Error("Failed to create verified key",
			zap.Error(err),
			zap.String("key", key),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeCreate"),
		)
		return err
	}
	fmt.Printf("DEBUG: VerifiedSet completed successfully - TX ID: %d\n", verifiedTx.Id)

	ic.Logger.Logger.Info("Transaction verified",
		zap.Uint64("tx", verifiedTx.Id),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.SafeCreate"),
	)
	fmt.Printf("=== DEBUG: SafeCreate completed successfully for key: %s ===\n", key)
	return nil
}

// SafeRead retrieves a value by key with cryptographic verification
func SafeRead(ic *config.ImmuClient, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	var PooledConnection *config.PooledConnection
	var err error
	var shouldReturnConnection bool = false

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeRead"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Returning connection to pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.SafeRead"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Log the read operation
	ic.Logger.Logger.Info("Reading verified key from database",
		zap.String("key", key),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.SafeRead"),
	)

	// Execute the read directly (no withRetry for pooled connections)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entry, err := ic.Client.VerifiedGet(ctx, []byte(key))
	if err != nil {
		if isNotFoundError(err) {
			ic.Logger.Logger.Warn("Key not found",
				zap.String("key", key),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.SafeRead"),
			)
			return nil, ErrNotFound
		}
		ic.Logger.Logger.Error("Database read failed",
			zap.Error(err),
			zap.String("key", key),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeRead"),
		)
		return nil, fmt.Errorf("database read failed: %w", err)
	}

	ic.Logger.Logger.Info("Successfully read and verified key",
		zap.String("key", key),
		zap.Uint64("transaction_id", entry.Tx),
		zap.Int("value_length", len(entry.Value)),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.SafeRead"),
	)

	entryValue = entry.Value

	return entryValue, nil
}

// SafeReadJSON retrieves a verified value by key and unmarshals it into dest
func SafeReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}

	// Log the operation start
	var logger *zap.Logger
	if ic != nil {
		logger = ic.Logger.Logger
		logger.Info("Unmarshaling verified JSON data",
			zap.String("key", key),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeReadJSON"),
		)
	}

	// Get the data using SafeRead
	data, err := SafeRead(ic, key)
	if err != nil {
		if logger != nil {
			logger.Error("Failed to read JSON data",
				zap.String("key", key),
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.SafeReadJSON"),
			)
		}
		return err
	}

	// Unmarshal the JSON data
	if err := json.Unmarshal(data, dest); err != nil {
		if logger != nil {
			logger.Error("Failed to unmarshal JSON data",
				zap.String("key", key),
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.SafeReadJSON"),
			)
		}
		return fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	if logger != nil {
		logger.Info("Successfully unmarshaled JSON data",
			zap.String("key", key),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.SafeReadJSON"),
		)
	}

	return nil
}

// GetHistory retrieves the history of values for a key
func GetHistory(ic *config.ImmuClient, key string, limit int) ([]*schema.Entry, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var entries []*schema.Entry
	var PooledConnection *config.PooledConnection
	var err error
	shouldReturnConnection := false

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
	}

	// Ensure we return the connection if we acquired it
	defer func() {
		if shouldReturnConnection && PooledConnection != nil {
			PooledConnection.Client.Logger.Logger.Info("Returning connection to pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetHistory"),
			)
			PutMainDBConnection(PooledConnection)
		}
	}()

	// Log the history request
	ic.Logger.Logger.Info("Getting history for key",
		zap.String("key", key),
		zap.Int("limit", limit),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetHistory"),
	)

	// Execute the history request with retry logic
	err = withRetry(ic, "GetHistory", func() error {
		historyReq := &schema.HistoryRequest{
			Key:   []byte(key),
			Limit: int32(limit),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		historyResp, err := ic.Client.History(ctx, historyReq)
		if err != nil {
			ic.Logger.Logger.Error("Failed to get history",
				zap.String("key", key),
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetHistory"),
			)
			return fmt.Errorf("failed to get history: %w", err)
		}

		entries = historyResp.Entries

		ic.Logger.Logger.Info("Successfully retrieved history",
			zap.String("key", key),
			zap.Int("entry_count", len(entries)),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetHistory"),
		)

		return nil
	})

	if err != nil {
		ic.Logger.Logger.Error("Failed to get history - retry attempts failed and limit exceeded",
			zap.String("key", key),
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetHistory"),
		)
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

// GetDatabaseState returns the current state of the database
func GetDatabaseState(ic *config.ImmuClient) (*schema.ImmutableState, error) {
	var state *schema.ImmutableState
	var PooledConnection *config.PooledConnection
	var err error
	shouldReturnConnection := false

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Logger.Info("Client Connection is Nil, so Pulled up quick connection from the Pool",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetDatabaseState"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Logger.Info("Returning connection to pool",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetDatabaseState"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Log the operation
	ic.Logger.Logger.Info("Getting current database state",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetDatabaseState"),
	)

	// Execute the operation directly (no withRetry for pooled connections)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err = ic.Client.CurrentState(ctx)
	if err != nil {
		ic.Logger.Logger.Error("Failed to get database state",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetDatabaseState"),
		)
		return nil, err
	}

	ic.Logger.Logger.Info("Successfully retrieved database state",
		zap.Uint64("tx_id", state.TxId),
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetDatabaseState"),
	)

	return state, nil
}

// Exists checks if a key exists in the database (UNCHANGED)
func Exists(PooledConnection *config.PooledConnection, key string) (bool, error) {
	if key == "" {
		PooledConnection.Client.Logger.Logger.Info("Key is empty",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Exists"),
		)
		return false, ErrEmptyKey
	}

	_, err := Read(PooledConnection, key)
	if err != nil {
		// Check if it's a "key not found" error
		if err == ErrNotFound || strings.Contains(err.Error(), "key not found") {
			PooledConnection.Client.Logger.Logger.Info("Key does not exist",
				zap.String("key", key),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.Exists"),
			)
			return false, nil
		}
		PooledConnection.Client.Logger.Logger.Error("Failed to read key",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Exists"),
		)
		return false, err
	}
	PooledConnection.Client.Logger.Logger.Info("Key exists",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.Exists"),
	)
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
		ic.Logger.Logger.Info("Executing transaction with %d operations",
			zap.Int("operations", len(tx.Ops)),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Transaction"),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := ic.Client.ExecAll(ctx, &schema.ExecAllRequest{
			Operations: tx.Ops,
		})

		if err != nil {
			return err
		}

		ic.Logger.Logger.Info("Transaction with %d operations executed successfully",
			zap.Int("operations", len(tx.Ops)),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Transaction"),
		)
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
		return false
	}
	if !ic.IsConnected {
		return false
	}

	// Try to get current state as a health check
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ic.Client.CurrentState(ctx)
	return err == nil
}

// Ping performs a health check on the database
func Ping(ic *config.ImmuClient) error {
	// Handle nil or invalid connection
	if ic == nil {
		return fmt.Errorf("database client is nil")
	}

	// Log the ping attempt
	ic.Logger.Logger.Info("Pinging database",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.Ping"),
	)

	// Execute the ping with retry logic
	err := withRetry(ic, "Ping", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := ic.Client.CurrentState(ctx)
		if err != nil {
			ic.Logger.Logger.Error("Database ping failed",
				zap.Error(err),
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.Ping"),
			)
			return fmt.Errorf("database ping failed: %w", err)
		}

		ic.Logger.Logger.Info("Database ping successful",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Ping"),
		)
		return nil
	})

	if err != nil {
		ic.Logger.Logger.Error("Database ping failed after retries",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.Ping"),
		)
		return err
	}

	return nil
}

// StoreZKBlock stores a complete ZK block in the main database (UNCHANGED)
func StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error {
	var err error
	var shouldReturnConnection bool = false
	// Create a unique key for the block
	blockKey := fmt.Sprintf("%s%d", PREFIX_BLOCK, block.BlockNumber)

	// Ensure the block is not already stored
	if exists, err := Exists(mainDBClient, blockKey); err != nil {
		return fmt.Errorf("failed to check if block %d exists: %w", block.BlockNumber, err)
	} else if exists {
		return fmt.Errorf("block %d already exists", block.BlockNumber)
	}

	if mainDBClient == nil {
		// Pull up quick connection
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
		zap.String(logging.Connection_database, config.DBName),
		zap.String("blockkey", blockKey),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
	)
	// ensuer mainDB is selected
	err = ensureMainDBSelected(mainDBClient)
	if err != nil {
		return fmt.Errorf("failed to ensure main DB is selected: %w", err)
	}

	// Store the full block data
	if err := SafeCreate(mainDBClient.Client, blockKey, block); err != nil {
		return fmt.Errorf("failed to store block %d: %w", block.BlockNumber, err)
	}

	// Also store by hash for lookups
	hashKey := fmt.Sprintf("%s%s", PREFIX_BLOCK_HASH, block.BlockHash.Hex())
	if err := Create(mainDBClient, hashKey, blockKey); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to store block hash mapping",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
		)
		return fmt.Errorf("failed to store block hash mapping: %w", err)
	}
	mainDBClient.Client.Logger.Logger.Info("Successfully stored block hash mapping",
		zap.String("blockkey", blockKey),
		zap.String("blockhash", block.BlockHash.Hex()),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
	)

	// Store the latest block number for quick access
	if err := Create(mainDBClient, "latest_block", block.BlockNumber); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to update latest block",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
		)
		return fmt.Errorf("failed to update latest block: %w", err)
	}

	// Store each transaction hash -> block number mapping for lookups
	for _, tx := range block.Transactions {
		txKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_TX, tx.Hash)
		mainDBClient.Client.Logger.Logger.Info("Storing tx mapping",
			zap.String("txkey", txKey),
			zap.String("blockkey", blockKey),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
		)

		if err := Create(mainDBClient, txKey, block.BlockNumber); err != nil {
			return fmt.Errorf("failed to store tx mapping for %s: %w", tx.Hash, err)
		}
	}

	mainDBClient.Client.Logger.Logger.Info("Successfully stored block %d with hash %s and %d transactions",
		zap.String("blockkey", blockKey),
		zap.String("blockhash", block.BlockHash.Hex()),
		zap.Int("transactions", len(block.Transactions)),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.StoreZKBlock"),
	)

	return nil
}

// GetZKBlockByNumber retrieves a ZK block by its number (UNCHANGED)
func GetZKBlockByNumber(mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	var shouldReturnConnection bool = false
	var err error
	blockKey := fmt.Sprintf("%s%d", PREFIX_BLOCK, blockNumber)

	block := new(config.ZKBlock)
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetZKBlockByNumber"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetZKBlockByNumber"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	if err := SafeReadJSON(mainDBClient.Client, blockKey, block); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to retrieve block",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetZKBlockByNumber"),
		)
		return nil, fmt.Errorf("failed to retrieve block %d: %w", blockNumber, err)
	}

	return block, nil
}

// GetZKBlockByHash retrieves a ZK block by its hash (UNCHANGED)
func GetZKBlockByHash(mainDBClient *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	// First get the block number from the hash
	var shouldReturnConnection bool = false
	var err error
	hashKey := fmt.Sprintf("%s%s", PREFIX_BLOCK_HASH, blockHash)
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetZKBlockByHash"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetZKBlockByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	blockKeyBytes, err := Read(mainDBClient, hashKey)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to find block with hash",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetZKBlockByHash"),
		)
		return nil, fmt.Errorf("failed to find block with hash %s: %w", blockHash, err)
	}

	// Then get the block using the block key
	blockKey := string(blockKeyBytes)

	block := new(config.ZKBlock)
	if err := SafeReadJSON(mainDBClient.Client, blockKey, block); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to retrieve block by hash",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetZKBlockByHash"),
		)
		return nil, fmt.Errorf("failed to retrieve block by hash %s: %w", blockHash, err)
	}
	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved block by hash",
		zap.String("blockkey", blockKey),
		zap.String("blockhash", blockHash),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetZKBlockByHash"),
	)
	return block, nil
}

// GetLatestBlockNumber returns the latest block number (UNCHANGED)
func GetLatestBlockNumber(mainDBClient *config.PooledConnection) (uint64, error) {
	var err error
	var shouldReturnConnection bool = false
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return 0, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	latestBytes, err := Read(mainDBClient, "latest_block")
	if err != nil {
		// Check for both our custom ErrNotFound and the ImmuDB-specific errors
		if err == ErrNotFound ||
			strings.Contains(err.Error(), "key not found") ||
			strings.Contains(err.Error(), "tbtree: key not found") {
			mainDBClient.Client.Logger.Logger.Info("No blocks found in the database yet",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
			)
			return 0, nil // No blocks yet
		}
		mainDBClient.Client.Logger.Logger.Error("Failed to get latest block number",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
		)
		return 0, fmt.Errorf("failed to get latest block: %w", err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(latestBytes, &blockNumber); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to parse latest block number",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
		)
		return 0, fmt.Errorf("failed to parse latest block number: %w", err)
	}
	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved latest block number",
		zap.String("blocknumber", fmt.Sprintf("%d", blockNumber)),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetLatestBlockNumber"),
	)
	return blockNumber, nil
}

// GetTransactionBlock returns the block containing a specific transaction (UNCHANGED)
func GetTransactionBlock(mainDBClient *config.PooledConnection, txHash string) (*config.ZKBlock, error) {
	var err error
	var shouldReturnConnection bool = false
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionBlock"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetTransactionBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	txKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_TX, txHash)

	blockNumberBytes, err := Read(mainDBClient, txKey)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to find block with hash",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionBlock"),
		)
		return nil, fmt.Errorf("transaction %s not found: %w", txHash, err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(blockNumberBytes, &blockNumber); err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to parse block number for tx",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionBlock"),
		)
		return nil, fmt.Errorf("failed to parse block number for tx %s: %w", txHash, err)
	}

	return GetZKBlockByNumber(mainDBClient, blockNumber)
}

// Get Transaction by hash
func GetTransactionByHash(mainDBClient *config.PooledConnection, txHash string) (*config.Transaction, error) {
	// Get the block that contains the transaction.
	var err error
	var shouldReturnConnection bool = false
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	block, err := GetTransactionBlock(mainDBClient, txHash)
	if err != nil {
		mainDBClient.Client.Logger.Logger.Error("Failed to get transaction block",
			zap.Error(err),
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
		)
		return nil, fmt.Errorf("failed to get transaction block: %w", err)
	}

	// Find the transaction in the block.
	var zkTx *config.Transaction
	for i := range block.Transactions {
		TempBlockHash := block.Transactions[i].Hash.String()
		if TempBlockHash == txHash {
			mainDBClient.Client.Logger.Logger.Info("Successfully retrieved transaction by hash",
				zap.String("transactionhash", txHash),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
			)
			zkTx = &block.Transactions[i]
			break
		}
	}

	if zkTx == nil {
		mainDBClient.Client.Logger.Logger.Error("Transaction is nil",
			zap.String("transactionhash", txHash),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
		)
		return nil, fmt.Errorf("transaction %s not found in block %d", txHash, block.BlockNumber)
	}
	mainDBClient.Client.Logger.Logger.Info("Successfully retrieved transaction by hash",
		zap.String("transactionhash", txHash),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetTransactionByHash"),
	)
	return zkTx, nil
}

// GetTransactionsBatch fetches multiple transactions by their hashes in a single batch
func GetTransactionsBatch(mainDBClient *config.PooledConnection, hashes []string) ([]*config.Transaction, error) {
	var transactions []*config.Transaction
	var err error
	var shouldReturnConnection bool = false
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetTransactionsBatch"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetTransactionsBatch"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
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

func GetAllBlocks(mainDBClient *config.PooledConnection) ([]*config.ZKBlock, error) {
	var err error
	var shouldReturnConnection bool = false
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true
		mainDBClient.Client.Logger.Logger.Info("Main DB connection retrieved successfully",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.GetAllBlocks"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Logger.Info("Main DB connection put back successfully",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetAllBlocks"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
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

// BatchCreateOrdered stores multiple key-value pairs preserving order
func BatchCreateOrdered(PooledConnection *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}
	var err error
	var shouldReturnConnection bool = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnection()
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer PutMainDBConnection(PooledConnection)
	}
	// Ensure the database for this pooled connection is selected
	if err := ensureConnectionDatabaseSelected(PooledConnection); err != nil {
		return fmt.Errorf("database selection failed: %w", err)
	}
	ops := make([]*schema.Op, 0, len(entries))
	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return ErrEmptyKey
		}
		ops = append(ops, &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: []byte(e.Key), Value: e.Value}}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
	if err != nil {
		return fmt.Errorf("batch operation failed: %w", err)
	}
	return nil
}

// ensureConnectionDatabaseSelected selects the database associated with this pooled connection
func ensureConnectionDatabaseSelected(pc *config.PooledConnection) error {
	if pc == nil || pc.Client == nil || pc.Client.Client == nil {
		return fmt.Errorf("invalid pooled connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pc.Client.Client.UseDatabase(ctx, &schema.Database{DatabaseName: pc.Database})
	return err
}
