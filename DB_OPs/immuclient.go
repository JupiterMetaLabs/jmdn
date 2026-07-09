package DB_OPs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"gossipnode/config"

	DB_OPs_common "gossipnode/DB_OPs/common"
	GRO "gossipnode/config/GRO"

	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/status"
)

var ImmuclientLocalGRO interfaces.LocalGoroutineManagerInterface

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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Warn(loggerCtx, "Connection lost, attempting to reconnect before operation",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.reconnect"))
	// Clean up existing connection if any
	if ic.Cancel != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Warn(loggerCtx, "Canceling existing connection",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.reconnect"))
		ic.Cancel()
	}

	if ic.Client != nil {
		ic.Client.Disconnect()
	}

	ic.IsConnected = false

	ic.Logger.Warn(loggerCtx, "Connection marked as disconnected - calling code should handle reconnection",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.reconnect"))

	// Return error to indicate reconnection failed
	return fmt.Errorf("connection lost - cannot reconnect without getting new connection from pool")
}

// withRetry executes the given operation with retry logic (UNCHANGED)
func withRetry(ic *config.ImmuClient, operation string, fn func() error) error {
	var err error
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for attempt := 0; attempt <= ic.RetryLimit; attempt++ {
		// Debugging
		// fmt.Println("withRetry called, Function Name that called withRetry is ", operation)
		// Check connection status first
		if !ic.IsConnected {
			ic.Logger.Warn(loggerCtx, fmt.Sprintf("Connection lost, attempting to reconnect before %s operation", operation),
				ion.String("operation", operation),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.withRetry"))
			if err = reconnect(ic, operation); err != nil {
				ic.Logger.Error(loggerCtx, fmt.Sprintf("Reconnection attempt %d/%d failed: %v", attempt+1, ic.RetryLimit+1, err),
					err,
					ion.Int("attempt", attempt+1),
					ion.Int("retry_limit", ic.RetryLimit+1),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.withRetry"))
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
			ic.Logger.Warn(loggerCtx, fmt.Sprintf("%s operation failed due to connection issue (attempt %d/%d): %v", operation, attempt+1, ic.RetryLimit+1, err),
				ion.String("operation", operation),
				ion.Int("attempt", attempt+1),
				ion.Int("retry_limit", ic.RetryLimit+1),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.withRetry"))
			ic.IsConnected = false // Mark as disconnected

			// Try to reconnect, but don't retry if reconnection fails
			if reconnectErr := reconnect(ic, "withRetry"); reconnectErr != nil {
				ic.Logger.Error(loggerCtx, fmt.Sprintf("Reconnection failed during retry - cannot reconnect without getting new connection from pool: %v", reconnectErr),
					reconnectErr,
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.withRetry"))
				return fmt.Errorf("connection lost - cannot reconnect without getting new connection from pool: %w", reconnectErr)
			}

			if attempt < ic.RetryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}

		// Non-connection error or final attempt
		ic.Logger.Error(loggerCtx, fmt.Sprintf("%s operation failed (attempt %d/%d): %v", operation, attempt+1, ic.RetryLimit+1, err),
			err,
			ion.String("operation", operation),
			ion.Int("attempt", attempt+1),
			ion.Int("retry_limit", ic.RetryLimit+1),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.withRetry"))
		return fmt.Errorf("%s failed: %w", operation, err)
	}

	return err
}

// Create stores a value with the given key using the connection pool
func Create(PooledConnection *config.PooledConnection, key string, value interface{}) error {
	var ic *config.ImmuClient
	var shouldReturnConnection = false
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if key == "" {
		return ErrEmptyKey
	}

	if value == nil {
		return ErrNilValue
	}

	// Get a connection from the pool
	if PooledConnection == nil {
		var err error
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Create"))
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Create"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic = PooledConnection.Client
	// Ensure the database is selected
	if err := ensureMainDBSelected(PooledConnection); err != nil {
		ic.Logger.Error(loggerCtx, "Failed to ensure main database selected",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Create"))
		return fmt.Errorf("database selection failed: %w", err)
	}

	// Convert value to bytes
	valueBytes, err := toBytes(value)
	if err != nil {
		ic.Logger.Error(loggerCtx, "Failed to convert value to bytes",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Create"))
		return fmt.Errorf("failed to convert value to bytes: %w", err)
	}

	// Log the operation
	ic.Logger.Debug(loggerCtx, "Creating key",
		ion.String("key", key),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Create"))

	// Store the key-value pair
	_, err = ic.Client.Set(ctx, []byte(key), valueBytes)
	if err != nil {
		ic.Logger.Error(loggerCtx, "Failed to set key",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Create"))
		return fmt.Errorf("failed to set key %s: %w", key, err)
	}

	ic.Logger.Debug(loggerCtx, "Successfully created key",
		ion.String("key", key),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Create"))

	return nil
}

// Read retrieves a value by key using the connection pool
func Read(PooledConnection *config.PooledConnection, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var entryValue []byte
	var err error
	var shouldReturnConnection = false

	// Handle nil or invalid connection
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true

		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Read"))
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {

			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Read"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Ensure the database is selected
	if err := ensureMainDBSelected(PooledConnection); err != nil {

		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to ensure main database selected",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Read"))
		return nil, fmt.Errorf("database selection failed: %w", err)
	}

	// Log the read operation
	PooledConnection.Client.Logger.Debug(loggerCtx, "Reading key from database",
		ion.String("key", key),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Read"))

	// Execute the read operation

	var entry *schema.Entry
	err = withRetry(PooledConnection.Client, "Read", func() error {
		var innerErr error
		entry, innerErr = PooledConnection.Client.Client.Get(ctx, []byte(key))
		return innerErr
	})

	if err != nil {
		if isNotFoundError(err) {
			PooledConnection.Client.Logger.Warn(loggerCtx, "Key not found",
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Read"))
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("database read failed: %w", err)
	}

	entryValue = entry.Value

	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully read key",
		ion.String("key", key),
		ion.Int("value_length", len(entryValue)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Read"))

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
func ReadJSON(key string, dest interface{}) error {
	var err error
	var data []byte

	data, err = Read(nil, key)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value (UNCHANGED)
func Update(key string, value interface{}) error {
	return Create(nil, key, value)
}

// GetKeys retrieves keys with a specified prefix (UNCHANGED - but can optionally use connection pool)
func GetKeys(PooledConnection *config.PooledConnection, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var keys []string
	var err error
	var shouldReturnConnection = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetKeys"))
	}
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetKeys"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if PooledConnection != nil {
		// Use connection pool approach
		PooledConnection.Client.Logger.Debug(loggerCtx, fmt.Sprintf("Scanning keys with prefix: %s (limit: %d)", prefix, limit),
			ion.String("prefix", prefix),
			ion.Int("limit", limit),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetKeys"))
		scanReq := &schema.ScanRequest{
			Prefix: []byte(prefix),
			Limit:  uint64(limit),
			Desc:   true, // latest keys first
		}

		// Create a fresh context for the scan operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		scanResult, err := PooledConnection.Client.Client.Scan(ctx, scanReq)
		if err != nil {
			PooledConnection.Client.Logger.Error(loggerCtx, fmt.Sprintf("Failed to scan keys with prefix: %s (limit: %d)", prefix, limit),
				err,
				ion.String("prefix", prefix),
				ion.Int("limit", limit),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetKeys"))
			return nil, err
		}

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}

		PooledConnection.Client.Logger.Debug(loggerCtx, fmt.Sprintf("Found %d keys with prefix: %s", len(keys), prefix),
			ion.Int("count", len(keys)),
			ion.String("prefix", prefix),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetKeys"))
		return keys, nil
	}
	PooledConnection.Client.Logger.Debug(loggerCtx, fmt.Sprintf("Found %d keys with prefix: %s", len(keys), prefix),
		ion.Int("count", len(keys)),
		ion.String("prefix", prefix),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetKeys"))
	return keys, nil
}

func GetAllKeys(PooledConnection *config.PooledConnection, prefix string) ([]string, error) {
	var allKeys []string
	batchSize := 1000
	// Define Function wide context for timeout - Increased to 10m for large syncs
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lastKey []byte
	var err error
	var shouldReturnConnection = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w - GetAllKeys", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetAllKeys"))
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetAllKeys"))
			PutMainDBConnection(PooledConnection)
		}()
	}

	batchNum := 0
	lastSeenKey := ""    // Track the last key we've seen to detect infinite loops
	maxBatches := 100000 // Safety limit to prevent infinite loops (100M keys max)

	for batchNum < maxBatches {
		batchNum++

		var rawKeys []string

		// Retry loop for connection errors
		for retry := 0; retry < 3; retry++ {
			rawKeys, err = getKeysBatch(PooledConnection, prefix, batchSize, lastKey)
			if err != nil {
				// Check for connection errors
				errStr := err.Error()
				if retry < 2 && (strings.Contains(errStr, "closing transport") ||
					strings.Contains(errStr, "EOF") ||
					strings.Contains(errStr, "Unavailable") ||
					strings.Contains(errStr, "connection error")) {

					PooledConnection.Client.Logger.Warn(loggerCtx, fmt.Sprintf("connection error in batch %d (attempt %d) — refreshing", batchNum, retry+1),
						ion.String("error", err.Error()),
						ion.String("function", "DB_OPs.GetAllKeys"))

					// If we own the connection (shouldReturnConnection=true) or even if we don't,
					// we need a working connection to continue.
					// If we don't own it, we can't easily "fix" the caller's reference, but we can update ours locally.

					// Get a new healthy connection
					// Use a fresh context for the connection, but link it to the operation timeout
					newConn, connErr := GetMainDBConnectionandPutBack(ctx)
					if connErr == nil {
						// Clean up old connection if we owned it?
						// PutMainDBConnection is safe to call if we owned it.
						// Even if we didn't own it, it's dead, so returning it to pool (where it might be checked/closed) is fine.
						PutMainDBConnection(PooledConnection)

						PooledConnection = newConn
						// Now we definitely own this new connection (at least locally)
						// Update cleanup flag if it wasn't set
						if !shouldReturnConnection {
							shouldReturnConnection = true
							// Add cleanup defer for this new connection?
							// The exiting defer will handle `PooledConnection` variable value at exit.
							// So just updating PooledConnection is sufficient for the defer to close the *new* one.
						}
						continue // Retry with new connection
					} else {
						PooledConnection.Client.Logger.Warn(loggerCtx, "failed to refresh connection",
							ion.String("error", connErr.Error()),
							ion.String("function", "DB_OPs.GetAllKeys"))
					}
				}

				// If not retryable or retries exhausted
				PooledConnection.Client.Logger.Error(loggerCtx, fmt.Sprintf("Failed to scan keys with prefix: %s (limit: %d)", prefix, batchSize),
					err,
					ion.String("prefix", prefix),
					ion.Int("limit", batchSize),
					ion.String("database", config.DBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetKeys"))
				return nil, fmt.Errorf("failed to get batch %d for prefix '%s': %v - GetAllKeys (retried %d times)", batchNum, prefix, err, retry)
			}
			// Success
			break
		}

		// Validate that all keys match the prefix (SeekKey might cause issues)
		validKeys := make([]string, 0, len(rawKeys))
		for _, key := range rawKeys {
			if strings.HasPrefix(key, prefix) {
				validKeys = append(validKeys, key)
			} else {
				// If we get a key that doesn't match the prefix, we've gone past the prefix range
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
			}
		}

		// If no keys returned, we're done
		if len(uniqueKeys) == 0 {
			break
		}

		// Add keys to our result
		allKeys = append(allKeys, uniqueKeys...)

		// Use uniqueKeys for the rest of the logic
		keys := uniqueKeys

		// Check if the first key is the same as lastSeenKey (overlap from SeekKey)
		// ImmuDB Scan is inclusive of SeekKey, so we must skip it to advance
		if len(keys) > 0 && lastSeenKey != "" && keys[0] == lastSeenKey {
			keys = keys[1:]
			if len(keys) == 0 {
				break
			}
		}

		// If we got fewer than batch size from the DB, we're done
		if len(rawKeys) < batchSize {
			break
		}

		// Set last key for next iteration - use the last key from this batch
		newLastKey := keys[len(keys)-1]

		// Check if we're stuck - if the last key hasn't changed, we're in a loop
		if lastSeenKey != "" && newLastKey == lastSeenKey {
			PooledConnection.Client.Logger.Warn(loggerCtx, "last key unchanged — stopping to prevent infinite loop",
				ion.String("last_key", newLastKey),
				ion.String("function", "DB_OPs.GetAllKeys"))
			break
		}

		lastKey = []byte(newLastKey)
		lastSeenKey = newLastKey
	}

	if batchNum >= maxBatches {
		PooledConnection.Client.Logger.Warn(loggerCtx, "reached max batch limit — possible pagination issue",
			ion.Int("max_batches", maxBatches),
			ion.String("prefix", prefix),
			ion.Int("keys_collected", len(allKeys)),
			ion.String("function", "DB_OPs.GetAllKeys"))
	}
	return allKeys, nil
}

// CountTransactionsByAccount counts the number of transactions for a specific account
func CountTransactionsByAccount(accountAddr *common.Address) (int64, error) {
	// Get all transactions for this account
	transactions, err := GetTransactionsByAccount(nil, accountAddr)
	if err != nil {
		return 0, fmt.Errorf("failed to get transactions for account: %w - CountTransactionsByAccount", err)
	}

	return int64(len(transactions)), nil
}

// CountTransactions counts the total number of transactions in the database.
func CountTransactions(PooledConnection *config.PooledConnection) (int, error) {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var err error
	var shouldReturnConnection = false
	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return -1, fmt.Errorf("failed to get database connection: %w - CountTransactions", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.CountTransactions"))
	}
	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.CountTransactions"))
			PutMainDBConnection(PooledConnection)
		}()
	}
	// This function will scan for keys with the "tx:" prefix and count them.
	// It's more efficient than fetching all keys.
	count, err := CountBuilder{}.GetMainDBCount(DEFAULT_PREFIX_TX)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Helper function to get a batch of keys (UNCHANGED - but can optionally use connection pool)
func getKeysBatch(PooledConnection *config.PooledConnection, prefix string, limit int, seekKey []byte) ([]string, error) {
	var keys []string
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		// If no connection then quickly pull connection from the pool
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w - getKeysBatch", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.getKeysBatch"))
	}

	if shouldReturnConnection {
		defer func() {
			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.getKeysBatch"))
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
			Desc:    false, // ASC: Prefix filter is reliable only in ascending scans;
			// DESC with no matching keys falls backward past the prefix and returns wrong results
		}

		ic.Logger.Debug(loggerCtx, fmt.Sprintf("Scanning keys with prefix: %s (limit: %d)", prefix, limit),
			ion.String("prefix", prefix),
			ion.Int("limit", limit),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.getKeysBatch"))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		scanResult, err := ic.Client.Scan(ctx, scanReq)
		if err != nil {
			ic.Logger.Error(loggerCtx, fmt.Sprintf("Failed to scan keys with prefix: %s (limit: %d)", prefix, limit),
				err,
				ion.String("prefix", prefix),
				ion.Int("limit", limit),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.getKeysBatch"))
			return nil, fmt.Errorf("failed to scan keys with prefix: %s (limit: %d): %w - getKeysBatch", prefix, limit, err)
		}
		ic.Logger.Debug(loggerCtx, fmt.Sprintf("Scan completed for prefix: %s (limit: %d)", prefix, limit),
			ion.String("prefix", prefix),
			ion.Int("limit", limit),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.getKeysBatch"))

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}

		ic.Logger.Debug(loggerCtx, "Keys scanned successfully",
			ion.String("prefix", prefix),
			ion.Int("limit", limit),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.getKeysBatch"))
		return keys, nil
	}

	PooledConnection.Client.Logger.Warn(loggerCtx, "no valid ImmuDB client",
		ion.String("prefix", prefix),
		ion.String("function", "DB_OPs.getKeysBatch"))
	return nil, nil
}

// BatchCreate stores multiple key-value pairs in a single transaction
func BatchCreate(PooledConnection *config.PooledConnection, entries map[string]interface{}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	var shouldReturnConnection = false

	// Handle nil or invalid connection
	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w - BatchCreate", err)
		}
		shouldReturnConnection = true
		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.BatchCreate"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {

			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.BatchCreate"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	// Ensure the database for this pooled connection is selected
	if err := ensureConnectionDatabaseSelected(PooledConnection); err != nil {
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to ensure database selected",
			err,
			ion.String("database", PooledConnection.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.BatchCreate"))
		return fmt.Errorf("database selection failed: %w - BatchCreate", err)
	}

	PooledConnection.Client.Logger.Debug(loggerCtx, "Creating batch of entries",
		ion.Int("entry_count", len(entries)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.BatchCreate"))

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
			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to prepare value for batch operation",
				err,
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.BatchCreate"))
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

	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{
		Operations: ops,
	})

	if err != nil {
		PooledConnection.Client.Logger.Error(loggerCtx, "Batch create operation failed",
			err,
			ion.Int("entry_count", len(entries)),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.BatchCreate"))
		return fmt.Errorf("batch operation failed: %w - BatchCreate", err)
	}

	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully created batch of entries",
		ion.Int("entry_count", len(entries)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.BatchCreate"))

	return nil
}

// Close closes the ImmuDB client connection (UNCHANGED)
func Close(ic *config.ImmuClient) error {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Closing ImmuDB connection",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Close"))

	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		err := ic.Client.Disconnect()
		if err != nil {
			ic.Logger.Error(loggerCtx, fmt.Sprintf("Error disconnecting from ImmuDB: %v", err),
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Close"))
			return fmt.Errorf("error disconnecting from ImmuDB: %w", err)
		}
	}

	ic.IsConnected = false

	ic.Logger.Debug(loggerCtx, "ImmuDB connection closed successfully",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Close"))

	// Close the logger - Close after Sync and dumping all the buffered logs
	// Close the logger - Close after Sync and dumping all the buffered logs
	defer func() {
		ic.Logger.Debug(loggerCtx, "ImmuDB connection closed successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Close"))
		// Logger sync is handled by ion internally
	}()

	return nil
}

// GetMerkleRoot returns the current database Merkle root
func GetMerkleRoot(PooledConnection *config.PooledConnection) ([]byte, error) {
	var err error
	var shouldReturnConnection = false
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get accounts database connection: %w - GetMerkleRoot", err)
		}
		shouldReturnConnection = true

		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetMerkleRoot"),
		)
	}

	if shouldReturnConnection {
		defer func() {

			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetMerkleRoot"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	state, err := PooledConnection.Client.Client.CurrentState(ctx)
	if err != nil {

		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get current state",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetMerkleRoot"),
		)
		return nil, fmt.Errorf("failed to get current state: %w - GetMerkleRoot", err)
	}

	PooledConnection.Client.Logger.Debug(loggerCtx, "Successfully retrieved current state",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetMerkleRoot"),
	)
	// Extract the Merkle root hash from the state
	return state.TxHash, nil
}

// SafeCreate stores a value with the given key and verifies the operation (UNCHANGED - but can optionally use connection pool)
func SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	var err error
	var PooledConnection *config.PooledConnection
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// If ic is nil, we need to determine which database to use based on context
	// For now, we'll default to accounts database since this is called from UpdateAccountBalance
	if ic == nil {
		PooledConnection, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return err
		}
		shouldReturnConnection = true
		ic = PooledConnection.Client

		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeCreate"),
		)
	}

	// Check for empty key and nil value
	if key == "" {

		PooledConnection.Client.Logger.Error(loggerCtx, "Empty key provided",
			errors.New("empty key provided"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeCreate"),
		)
		return ErrEmptyKey
	}

	// Check for nil value
	if value == nil {
		PooledConnection.Client.Logger.Error(loggerCtx, "Nil value provided",
			errors.New("nil value provided"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeCreate"),
		)
		return ErrNilValue
	}

	if shouldReturnConnection {
		defer func() {

			PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is returned to the Pool",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.SafeCreate"),
			)
			PutAccountsConnection(PooledConnection)
		}()
	}

	valueBytes, err := toBytes(value)
	if err != nil {
		return err
	}

	ic.Logger.Debug(loggerCtx, "Creating verified key",
		ion.String("key", key),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.SafeCreate"),
	)

	verifiedTx, err := ic.Client.VerifiedSet(ctx, []byte(key), valueBytes)
	if err != nil {
		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to create verified key",
			err,
			ion.String("key", key),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeCreate"),
		)
		return err
	}
	ic.Logger.Debug(loggerCtx, "Transaction verified",
		ion.Uint64("tx", verifiedTx.Id),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.SafeCreate"),
	)
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
	var shouldReturnConnection = false

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true

		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeRead"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {

			ic.Logger.Debug(loggerCtx, "Returning connection to pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.SafeRead"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic.Logger.Debug(loggerCtx, "Reading verified key from database",
		ion.String("key", key),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.SafeRead"),
	)

	// Execute the read with retry
	var entry *schema.Entry
	err = withRetry(ic, "SafeRead", func() error {
		var innerErr error
		entry, innerErr = ic.Client.VerifiedGet(ctx, []byte(key))
		return innerErr
	})
	if err != nil {
		if isNotFoundError(err) {

			ic.Logger.Warn(loggerCtx, "Key not found",
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.SafeRead"),
			)
			return nil, ErrNotFound
		}

		ic.Logger.Error(loggerCtx, "Database read failed",
			err,
			ion.String("key", key),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeRead"),
		)
		return nil, fmt.Errorf("database read failed: %w", err)
	}

	ic.Logger.Debug(loggerCtx, "Successfully read and verified key",
		ion.String("key", key),
		ion.Uint64("transaction_id", entry.Tx),
		ion.Int("value_length", len(entry.Value)),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.SafeRead"),
	)

	entryValue = entry.Value

	return entryValue, nil
}

// SafeReadJSON retrieves a verified value by key and unmarshals it into dest
func SafeReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Log the operation start
	var logger *ion.Ion
	if ic != nil {
		logger = ic.Logger
		ic.Logger.Debug(loggerCtx, "Unmarshaling verified JSON data",
			ion.String("key", key),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeReadJSON"),
		)
	}

	// Get the data using SafeRead
	data, err := SafeRead(ic, key)
	if err != nil {
		if logger != nil {
			logger.Error(loggerCtx, "Failed to read JSON data",
				err,
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.SafeReadJSON"),
			)
		}
		return err
	}

	// Unmarshal the JSON data
	if err := json.Unmarshal(data, dest); err != nil {
		if logger != nil {
			logger.Error(loggerCtx, "Failed to unmarshal JSON data",
				err,
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.SafeReadJSON"),
			)
		}
		return fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	if logger != nil {
		logger.Debug(loggerCtx, "Successfully unmarshaled JSON data",
			ion.String("key", key),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.SafeReadJSON"),
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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var entries []*schema.Entry
	var PooledConnection *config.PooledConnection
	var err error
	shouldReturnConnection := false

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main database connection: %w - GetHistory", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true
	}

	// Ensure we return the connection if we acquired it
	defer func() {
		if shouldReturnConnection && PooledConnection != nil {

			ic.Logger.Debug(loggerCtx, "Returning connection to pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetHistory"),
			)
			PutMainDBConnection(PooledConnection)
		}
	}()

	// Log the history request

	ic.Logger.Debug(loggerCtx, "Getting history for key",
		ion.String("key", key),
		ion.Int("limit", limit),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetHistory"),
	)

	// Execute the history request with retry logic
	err = withRetry(ic, "GetHistory", func() error {
		historyReq := &schema.HistoryRequest{
			Key:   []byte(key),
			Limit: int32(limit),
		}

		historyResp, err := ic.Client.History(ctx, historyReq)
		if err != nil {

			PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get history",
				err,
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetHistory"),
			)
			return fmt.Errorf("failed to get history: %w", err)
		}

		entries = historyResp.Entries

		ic.Logger.Debug(loggerCtx, "Successfully retrieved history",
			ion.String("key", key),
			ion.Int("entry_count", len(entries)),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetHistory"),
		)

		return nil
	})

	if err != nil {

		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get history - retry attempts failed and limit exceeded",
			err,
			ion.String("key", key),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetHistory"),
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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Handle nil or invalid connection
	if ic == nil {
		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main database connection: %w - GetDatabaseState", err)
		}
		ic = PooledConnection.Client
		shouldReturnConnection = true

		PooledConnection.Client.Logger.Debug(loggerCtx, "Client Connection is Nil, so Pulled up quick connection from the Pool",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetDatabaseState"),
		)
	}

	// Ensure we return the connection if we acquired it
	if shouldReturnConnection {
		defer func() {

			ic.Logger.Debug(loggerCtx, "Returning connection to pool",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetDatabaseState"),
			)
			PutMainDBConnection(PooledConnection)
		}()
	}

	ic.Logger.Debug(loggerCtx, "Getting current database state",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetDatabaseState"),
	)

	// Execute the operation directly (no withRetry for pooled connections)
	state, err = ic.Client.CurrentState(ctx)
	if err != nil {

		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to get database state",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetDatabaseState"),
		)
		return nil, fmt.Errorf("failed to get database state: %w - GetDatabaseState", err)
	}

	ic.Logger.Debug(loggerCtx, "Successfully retrieved database state",
		ion.Uint64("tx_id", state.TxId),
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetDatabaseState"),
	)

	return state, nil
}

// Exists checks if a key exists in the database (UNCHANGED)
func Exists(PooledConnection *config.PooledConnection, key string) (bool, error) {

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if key == "" {
		PooledConnection.Client.Logger.Debug(loggerCtx, "Key is empty",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Exists"),
		)
		return false, ErrEmptyKey
	}

	_, err := Read(PooledConnection, key)
	if err != nil {
		// Check if it's a "key not found" error
		if err == ErrNotFound || strings.Contains(err.Error(), "key not found") {

			PooledConnection.Client.Logger.Debug(loggerCtx, "Key does not exist",
				ion.String("key", key),
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Exists"),
			)
			return false, nil
		}

		PooledConnection.Client.Logger.Error(loggerCtx, "Failed to read key",
			errors.New("failed to read key"),
			ion.String("key", key),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Exists"),
		)
		return false, err
	}

	PooledConnection.Client.Logger.Debug(loggerCtx, "Key exists",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Exists"),
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
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ic.Logger.Debug(loggerCtx, "Executing transaction with %d operations",
			ion.Int("operations", len(tx.Ops)),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Transaction"),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := ic.Client.ExecAll(ctx, &schema.ExecAllRequest{
			Operations: tx.Ops,
		})

		if err != nil {
			return err
		}

		ic.Logger.Debug(loggerCtx, "Transaction with %d operations executed successfully",
			ion.Int("operations", len(tx.Ops)),
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Transaction"),
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
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Pinging database",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Ping"),
	)

	// Execute the ping with retry logic
	err := withRetry(ic, "Ping", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := ic.Client.CurrentState(ctx)
		if err != nil {
			ic.Logger.Error(loggerCtx, "Database ping failed",
				err,
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.Ping"),
			)
			return fmt.Errorf("database ping failed: %w", err)
		}

		ic.Logger.Debug(loggerCtx, "Database ping successful",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Ping"),
		)
		return nil
	})

	if err != nil {

		ic.Logger.Error(loggerCtx, "Database ping failed after retries",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Ping"),
		)
		return err
	}

	return nil
}

// StoreZKBlock stores a complete ZK block in the main database (UNCHANGED)
func StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error {
	var err error
	var shouldReturnConnection = false
	// Create a unique key for the block
	blockKey := fmt.Sprintf("%s%d", PREFIX_BLOCK, block.BlockNumber)

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Ensure the block is not already stored
	if exists, err := Exists(mainDBClient, blockKey); err != nil {
		return fmt.Errorf("failed to check if block %d exists: %w - StoreZKBlock", block.BlockNumber, err)
	} else if exists {
		return fmt.Errorf("block %d already exists", block.BlockNumber)
	}

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	if mainDBClient == nil {
		// Pull up quick connection
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get main DB connection: %w - StoreZKBlock", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreZKBlock"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.StoreZKBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}

	mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
		ion.String("database", config.DBName),
		ion.String("blockkey", blockKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.StoreZKBlock"),
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

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to store block hash mapping",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreZKBlock"),
		)
		return fmt.Errorf("failed to store block hash mapping: %w", err)
	}

	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully stored block hash mapping",
		ion.String("blockkey", blockKey),
		ion.String("blockhash", block.BlockHash.Hex()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.StoreZKBlock"),
	)

	// StoreZKBlock no longer writes `latest_block`. The old per-block blind
	// write here regressed the tip on any out-of-order store (PoTS WAL dump,
	// replays, sync workers) and advanced it for SKELETON blocks written by
	// header sync — which forced the headers writer into a snapshot/restore
	// dance that itself raced concurrent writers. Callers that store FULL
	// blocks own the marker via DB_OPs.UpdateLatestBlockMonotonic (live paths:
	// blockPropagation/broadcast; sync: data writer batch-end, catchup phase 8).

	// Store each transaction hash -> block number mapping for lookups
	for _, tx := range block.Transactions {
		txKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_TX, tx.Hash)

		mainDBClient.Client.Logger.Debug(loggerCtx, "Storing tx mapping",
			ion.String("txkey", txKey),
			ion.String("blockkey", blockKey),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.StoreZKBlock"),
		)

		if err := Create(mainDBClient, txKey, block.BlockNumber); err != nil {
			return fmt.Errorf("failed to store tx mapping for %s: %w", tx.Hash, err)
		}
	}

	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully stored block %d with hash %s and %d transactions",
		ion.String("blockkey", blockKey),
		ion.String("blockhash", block.BlockHash.Hex()),
		ion.Int("transactions", len(block.Transactions)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.StoreZKBlock"),
	)

	return nil
}

// GetZKBlockByNumber retrieves a ZK block by its number (UNCHANGED)
func GetZKBlockByNumber(mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	var shouldReturnConnection = false
	var err error
	blockKey := fmt.Sprintf("%s%d", PREFIX_BLOCK, blockNumber)

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	block := new(config.ZKBlock)
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetZKBlockByNumber", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByNumber"),
		)
	}

	if shouldReturnConnection {
		defer func() {

			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetZKBlockByNumber"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	// Use VerifiedGet directly to avoid overhead of SafeReadJSON
	// (context creation, logging, etc. in a hot path).
	// Reuse ctx (5 s timeout from above) — ctxV had no deadline and could hang
	// indefinitely if ImmuDB is slow or unresponsive.
	entry, err := mainDBClient.Client.Client.VerifiedGet(ctx, []byte(blockKey))
	if err != nil {
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to retrieve block (VerifiedGet)",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByNumber"),
		)
		return nil, fmt.Errorf("failed to retrieve block %d (VerifiedGet): %w", blockNumber, err)
	}

	if err := json.Unmarshal(entry.Value, block); err != nil {
		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to unmarshal block",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByNumber"),
		)
		return nil, fmt.Errorf("failed to unmarshal block %d: %w", blockNumber, err)
	}

	return block, nil
}

// ReadZKBlockByNumber retrieves a ZK block by number using plain Get (no proof generation).
// Use for sync/reconciliation paths where tamper-proof guarantees are not required.
// 5–10× faster than GetZKBlockByNumber for bulk reads.
//
// Time: O(1); Space: O(block size)
func ReadZKBlockByNumber(ctx context.Context, mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	var shouldReturnConnection = false
	var err error
	blockKey := fmt.Sprintf("%s%d", PREFIX_BLOCK, blockNumber)

	block := new(config.ZKBlock)
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - ReadZKBlockByNumber", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer func() {
			PutMainDBConnection(mainDBClient)
		}()
	}

	entry, err := mainDBClient.Client.Client.Get(ctx, []byte(blockKey))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve block %d: %w", blockNumber, err)
	}

	if err := json.Unmarshal(entry.Value, block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block %d: %w", blockNumber, err)
	}

	return block, nil
}

// GetZKBlockByHash retrieves a ZK block by its hash (UNCHANGED)
func GetZKBlockByHash(mainDBClient *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	// First get the block number from the hash
	var shouldReturnConnection = false
	var err error
	hashKey := fmt.Sprintf("%s%s", PREFIX_BLOCK_HASH, blockHash)
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetZKBlockByHash", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByHash"),
		)
	}

	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetZKBlockByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	blockKeyBytes, err := Read(mainDBClient, hashKey)
	if err != nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to find block with hash",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByHash"),
		)
		return nil, fmt.Errorf("failed to find block with hash %s: %w", blockHash, err)
	}

	// Then get the block using the block key
	blockKey := string(blockKeyBytes)

	block := new(config.ZKBlock)
	if err := SafeReadJSON(mainDBClient.Client, blockKey, block); err != nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to retrieve block by hash",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetZKBlockByHash"),
		)
		return nil, fmt.Errorf("failed to retrieve block by hash %s: %w", blockHash, err)
	}

	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved block by hash",
		ion.String("blockkey", blockKey),
		ion.String("blockhash", blockHash),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetZKBlockByHash"),
	)
	return block, nil
}

// ReadZKBlockByHash retrieves a ZK block by its hash using plain Get (no proof generation).
// Use for sync/reconciliation paths where tamper-proof guarantees are not required.
// Time: O(1); Space: O(block size)
func ReadZKBlockByHash(ctx context.Context, mainDBClient *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	var shouldReturnConnection = false
	var err error
	hashKey := fmt.Sprintf("%s%s", PREFIX_BLOCK_HASH, blockHash)

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - ReadZKBlockByHash", err)
		}
		shouldReturnConnection = true
	}

	if shouldReturnConnection {
		defer func() {
			PutMainDBConnection(mainDBClient)
		}()
	}

	// First get the block key from the hash mapping using plain read
	entryHash, err := mainDBClient.Client.Client.Get(ctx, []byte(hashKey))
	if err != nil {
		return nil, fmt.Errorf("failed to find block key for hash %s: %w", blockHash, err)
	}

	blockKey := entryHash.Value

	// Then get the actual block data using the block key
	entryBlock, err := mainDBClient.Client.Client.Get(ctx, blockKey)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve block data for hash %s: %w", blockHash, err)
	}

	block := new(config.ZKBlock)
	if err := json.Unmarshal(entryBlock.Value, block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block for hash %s: %w", blockHash, err)
	}

	return block, nil
}

// GetLatestBlockNumber returns the latest block number
func GetLatestBlockNumber(ctx context.Context, mainDBClient *config.PooledConnection) (uint64, error) {
	var err error
	var shouldReturnConnection = false

	if ctx == nil {
		ctx = context.Background()
	}

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get main DB connection: %w - GetLatestBlockNumber", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(ctx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestBlockNumber"),
		)
	}

	if shouldReturnConnection {
		defer func() {

			mainDBClient.Client.Logger.Debug(ctx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetLatestBlockNumber"),
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

			mainDBClient.Client.Logger.Debug(ctx, "No blocks found in the database yet",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetLatestBlockNumber"),
			)
			return 0, nil // No blocks yet
		}

		mainDBClient.Client.Logger.Error(ctx, "Failed to get latest block number",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestBlockNumber"),
		)
		return 0, fmt.Errorf("failed to get latest block: %w - GetLatestBlockNumber", err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(latestBytes, &blockNumber); err != nil {

		mainDBClient.Client.Logger.Error(ctx, "Failed to parse latest block number",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetLatestBlockNumber"),
		)
		return 0, fmt.Errorf("failed to parse latest block number: %w - GetLatestBlockNumber", err)
	}

	mainDBClient.Client.Logger.Debug(ctx, "Successfully retrieved latest block number",
		ion.String("blocknumber", fmt.Sprintf("%d", blockNumber)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetLatestBlockNumber"),
	)
	return blockNumber, nil
}

// GetTransactionBlock returns the block containing a specific transaction (UNCHANGED)
func GetTransactionBlock(ctx context.Context, mainDBClient *config.PooledConnection, txHash string) (*config.ZKBlock, error) {
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	loggerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetTransactionBlock", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionBlock"),
		)
	}
	if shouldReturnConnection {
		defer func() {

			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionBlock"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	// Normalize hash to lowercase — ImmuDB keys are stored lowercase (via common.Hash.String()).
	txKey := fmt.Sprintf("%s%s", DEFAULT_PREFIX_TX, strings.ToLower(txHash))

	blockNumberBytes, err := Read(mainDBClient, txKey)
	if err != nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to find block with hash",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionBlock"),
		)
		return nil, fmt.Errorf("transaction %s not found: %w", txHash, err)
	}

	var blockNumber uint64
	if err := json.Unmarshal(blockNumberBytes, &blockNumber); err != nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to parse block number for tx",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionBlock"),
		)
		return nil, fmt.Errorf("failed to parse block number for tx %s: %w", txHash, err)
	}

	return ReadZKBlockByNumber(ctx, mainDBClient, blockNumber)
}

// Get Transaction by hash
func GetTransactionByHash(mainDBClient *config.PooledConnection, txHash string) (*config.Transaction, error) {
	// Get the block that contains the transaction.
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetTransactionByHash", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionByHash"),
		)
	}
	if shouldReturnConnection {
		defer func() {

			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionByHash"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	block, err := GetTransactionBlock(ctx, mainDBClient, txHash)
	if err != nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Failed to get transaction block",
			err,
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionByHash"),
		)
		return nil, fmt.Errorf("failed to get transaction block: %w", err)
	}

	// Find the transaction in the block.
	// Normalize both sides to lowercase so EIP-55 checksum hashes from callers
	// still match the lowercase hex stored in block.Transactions[i].Hash.
	var zkTx *config.Transaction
	normalizedTxHash := strings.ToLower(txHash)
	for i := range block.Transactions {
		TempBlockHash := strings.ToLower(block.Transactions[i].Hash.String())
		if TempBlockHash == normalizedTxHash {

			mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved transaction by hash",
				ion.String("transactionhash", txHash),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionByHash"),
			)
			zkTx = &block.Transactions[i]
			break
		}
	}

	if zkTx == nil {

		mainDBClient.Client.Logger.Error(loggerCtx, "Transaction is nil",
			err,
			ion.String("transactionhash", txHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionByHash"),
		)
		return nil, fmt.Errorf("transaction %s not found in block %d", txHash, block.BlockNumber)
	}

	mainDBClient.Client.Logger.Debug(loggerCtx, "Successfully retrieved transaction by hash",
		ion.String("transactionhash", txHash),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetTransactionByHash"),
	)
	return zkTx, nil
}

// GetTransactionsBatch fetches multiple transactions by their hashes in a single batch
func GetTransactionsBatch(mainDBClient *config.PooledConnection, hashes []string) ([]*config.Transaction, error) {
	if ImmuclientLocalGRO == nil {
		var err error
		ImmuclientLocalGRO, err = DB_OPs_common.InitializeGRO(GRO.DB_OPsImmuclientLocal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var transactions []*config.Transaction
	var err error
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w - GetTransactionsBatch", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetTransactionsBatch"),
		)
	}
	if shouldReturnConnection {
		defer func() {

			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetTransactionsBatch"),
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
		wg, err := ImmuclientLocalGRO.NewFunctionWaitGroup(ctx, GRO.DB_OPsImmuclientWG)
		if err != nil {
			return nil, fmt.Errorf("failed to create function wait group: %w - GetTransactionsBatch", err)
		}
		var mu sync.Mutex
		var batchErr error

		for _, hash := range batch {
			ImmuclientLocalGRO.Go(GRO.DB_OPsImmuclientThread, func(ctx context.Context) error {

				tx, err := GetTransactionByHash(mainDBClient, hash)
				if err != nil {
					batchErr = fmt.Errorf("failed to fetch transaction %s: %w", hash, err)
					return fmt.Errorf("failed to fetch transaction %s: %w", hash, err)
				}

				mu.Lock()
				transactions = append(transactions, tx)
				mu.Unlock()
				return nil
			}, local.AddToWaitGroup(GRO.DB_OPsImmuclientWG))
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
	var shouldReturnConnection = false

	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get main DB connection: %w", err)
		}
		shouldReturnConnection = true

		mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection retrieved successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetAllBlocks"),
		)
	}
	if shouldReturnConnection {
		defer func() {
			mainDBClient.Client.Logger.Debug(loggerCtx, "Main DB connection put back successfully",
				ion.String("database", config.DBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.GetAllBlocks"),
			)
			PutMainDBConnection(mainDBClient)
		}()
	}
	latestBlockNumber, err := GetLatestBlockNumber(ctx, mainDBClient)
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
	var shouldReturnConnection = false
	// Define Function wide context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if PooledConnection == nil || PooledConnection.Client == nil {

		PooledConnection, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("failed to get database connection: %w - BatchCreateOrdered", err)
		}
		shouldReturnConnection = true
	}
	if shouldReturnConnection {
		defer PutMainDBConnection(PooledConnection)
	}
	// Ensure the database for this pooled connection is selected
	if err := ensureConnectionDatabaseSelected(PooledConnection); err != nil {
		return fmt.Errorf("database selection failed: %w - BatchCreateOrdered", err)
	}
	ops := make([]*schema.Op, 0, len(entries))
	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return ErrEmptyKey
		}
		ops = append(ops, &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: []byte(e.Key), Value: e.Value}}})
	}
	_, err = PooledConnection.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
	if err != nil {
		return fmt.Errorf("batch operation failed: %w - BatchCreateOrdered", err)
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
