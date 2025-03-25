package DB_OPs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gossipnode/config"
	"log"
	"os"
	"path/filepath"
	// "sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Custom errors
var (
	ErrEmptyKey       = errors.New("key cannot be empty")
	ErrEmptyBatch     = errors.New("entries map cannot be empty")
	ErrNilValue       = errors.New("value cannot be nil")
	ErrNotFound       = errors.New("key not found")
	ErrConnectionLost = errors.New("connection to immudb lost")
)

// NewAsyncLogger creates a new async logger that writes to logs/ImmuDB.log
func NewAsyncLogger() (*config.AsyncLogger, error) {
	// Ensure logs directory exists
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Open log file
	logFilePath := filepath.Join(logDir, "ImmuDB.log")
	file, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Create logger
	logger := log.New(file, "", log.LstdFlags)
	
	// Create async logger
	asyncLogger := &config.AsyncLogger{
		Logger:  logger,
		LogChan: make(chan string, 1000), // Buffer up to 1000 log messages
		File:    file,
	}
	
	// Start background worker
	asyncLogger.Wg.Add(1)
	go config.ProcessLogs(asyncLogger)
	
	return asyncLogger, nil
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

// New creates and returns a connected ImmuClient
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

	// Establish connection
	err = connect(ic)
	if err != nil {
		config.Close(ic.Logger)
		return nil, err
	}

	return ic, nil
}

// connect establishes a connection to ImmuDB
func connect(ic *config.ImmuClient) error {
    config.Info(ic.Logger, "Connecting to ImmuDB at %s:%d", config.DBAddress, config.DBPort)
    
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort)

    c, err := client.NewImmuClient(opts)
    if err != nil {
        return fmt.Errorf("failed to create client: %w", err)
    }

    // Create context with timeout
    ctx, cancel := context.WithTimeout(ic.BaseCtx, config.RequestTimeout)
    
    // Login to immudb
    config.Info(ic.Logger, "Authenticating with ImmuDB")
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
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
    config.Info(ic.Logger, "Selecting database: %s", config.DBName)
    _, err = c.UseDatabase(ctx, &schema.Database{DatabaseName: config.DBName})
    if err != nil {
        cancel()
        c.Disconnect()
        return fmt.Errorf("failed to use database %s: %w", config.DBName, err)
    }

    ic.Client = c
    ic.Ctx = ctx
    ic.Cancel = cancel
    ic.IsConnected = true
    config.Info(ic.Logger, "Successfully connected to ImmuDB")

    return nil
}

// reconnect attempts to reestablish a lost connection
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

// withRetry executes the given operation with retry logic
func withRetry(ic *config.ImmuClient,operation string, fn func() error) error {
	var err error
	
	for attempt := 0; attempt <= ic.RetryLimit; attempt++ {
		// Check connection status first
		if !ic.IsConnected {
			config.Warning(ic.Logger,"Connection lost, attempting to reconnect before %s operation", operation)
			if err = reconnect(ic); err != nil {
				config.Error(ic.Logger,"Reconnection attempt %d/%d failed: %v", attempt+1, ic.RetryLimit+1, err)
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
			config.Warning(ic.Logger,"%s operation failed due to connection issue (attempt %d/%d): %v", 
				operation, attempt+1, ic.RetryLimit+1, err)
			ic.IsConnected = false
			if attempt < ic.RetryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}
		
		// Non-connection error or final attempt
		config.Error(ic.Logger,"%s operation failed (attempt %d/%d): %v", 
			operation, attempt+1, ic.RetryLimit+1, err)
		return fmt.Errorf("%s failed: %w", operation, err)
	}
	
	return err
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
		case 1:  // Cancelled
			return true
		case 4:  // DeadlineExceeded
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

// Create stores a value with the given key
func Create(ic *config.ImmuClient, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	
	if value == nil {
		return ErrNilValue
	}

	return withRetry(ic, "Create", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		config.Info(ic.Logger,"Creating key: %s", key)
		// Store the key-value pair
		_, err = ic.Client.Set(ic.Ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}
		
		config.Info(ic.Logger,"Successfully created key: %s", key)
		return nil
	})
}

// Read retrieves a value by key
func Read(ic *config.ImmuClient, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	
	err := withRetry(ic, "Read", func() error {
		config.Info(ic.Logger,"Reading key: %s", key)
		entry, err := ic.Client.Get(ic.Ctx, []byte(key))
		if err != nil {
			if err.Error() == "key not found" {
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

// ReadJSON retrieves a value by key and unmarshals it into dest
func ReadJSON(ic *config.ImmuClient,key string, dest interface{}) error {
	data, err := Read(ic, key)
	if err != nil {
		return err
	}

	config.Info(ic.Logger,"Unmarshaling JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value
func Update(ic *config.ImmuClient, key string, value interface{}) error {
	// In ImmuDB, update is the same as create since it's an immutable database
	// We simply write the new value with the same key
	config.Info(ic.Logger,"Updating key: %s", key)
	return Create(ic, key, value)
}

// GetKeys retrieves keys with a specified prefix
func GetKeys(ic *config.ImmuClient, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var keys []string
	
	err := withRetry(ic, "GetKeys", func() error {
		config.Info(ic.Logger,"Scanning keys with prefix: %s (limit: %d)", prefix, limit)
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

// BatchCreate stores multiple key-value pairs in a single transaction
func BatchCreate(ic *config.ImmuClient, entries map[string]interface{}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}

	return withRetry(ic, "BatchCreate", func() error {
		config.Info(ic.Logger,"Creating batch of %d entries", len(entries))
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

// Close closes the ImmuDB client connection
func Close(ic *config.ImmuClient) error {
	config.Info(ic.Logger,"Closing ImmuDB connection")
	
	if ic.Cancel != nil {
		ic.Cancel()
	}
	
	if ic.Client != nil {
		err := ic.Client.Disconnect()
		if err != nil {
			config.Error(ic.Logger,"Error disconnecting from ImmuDB: %v", err)
			return fmt.Errorf("error disconnecting from ImmuDB: %w", err)
		}
	}
	
	ic.IsConnected = false
	config.Info(ic.Logger,"ImmuDB connection closed successfully")
	
	// Close the logger
	err := config.Close(ic.Logger)
	if err != nil {
		return fmt.Errorf("error closing logger: %w", err)
	}
	
	return nil
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

// GetMerkleRoot returns the current database Merkle root
func GetMerkleRoot(ic *config.ImmuClient) ([]byte, error) {
	var merkleRoot []byte
	
	err := withRetry(ic, "GetMerkleRoot", func() error {
		config.Info(ic.Logger,"Getting current Merkle root")
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

// SafeCreate stores a value with the given key and verifies the operation
func SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	
	if value == nil {
		return ErrNilValue
	}

	return withRetry(ic, "SafeCreate", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		config.Info(ic.Logger,"Creating verified key: %s", key)
		// Store the key-value pair with verification
		verifiedTx, err := ic.Client.VerifiedSet(ic.Ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}

		config.Info(ic.Logger,"Transaction verified: tx=%d, verified=%v", 
			verifiedTx.Id, verifiedTx)
		return nil
	})
}

// SafeRead retrieves a value by key with cryptographic verification
func SafeRead(ic *config.ImmuClient, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	
	err := withRetry(ic, "SafeRead", func() error {
		config.Info(ic.Logger,"Reading verified key: %s", key)
		entry, err := ic.Client.VerifiedGet(ic.Ctx, []byte(key))
		if err != nil {
			if err.Error() == "key not found" {
				return ErrNotFound
			}
			return err
		}
		
		config.Info(ic.Logger,"Value verified: tx=%d, verified=%v", 
			entry.Tx, entry)
		
		entryValue = entry.Value
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return entryValue, nil
}

// SafeReadJSON retrieves a verified value by key and unmarshals it into dest
func SafeReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	data, err := SafeRead(ic, key)
	if err != nil {
		return err
	}

	config.Info(ic.Logger,"Unmarshaling verified JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// GetHistory retrieves the history of values for a key
func GetHistory(ic *config.ImmuClient,key string, limit int) ([]*schema.Entry, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var entries []*schema.Entry
	
	err := withRetry(ic, "GetHistory", func() error {
		config.Info(ic.Logger,"Getting history for key: %s (limit: %d)", key, limit)
		historyReq := &schema.HistoryRequest{
			Key:   []byte(key),
			Limit: int32(limit),
		}

		historyResp, err := ic.Client.History(ic.Ctx, historyReq)
		if err != nil {
			return err
		}
		
		entries = historyResp.Entries
		config.Info(ic.Logger,"Found %d historical entries for key: %s", len(entries), key)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return entries, nil
}



// NewBlockHasher creates a new BlockHasher
func NewBlockHasher() *config.BlockHasher {
	return &config.BlockHasher{}
}

// HashBlock generates a hash for a block using the nonce, sender, and timestamp
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
	
	err := withRetry(ic,"GetDatabaseState", func() error {
		config.Info(ic.Logger,"Getting current database state")
		// Get current state from server
		dbState, err := ic.Client.CurrentState(ic.Ctx)
		if err != nil {
			return err
		}
		
		state = dbState
		config.Info(ic.Logger,"Database state retrieved: TxId=%d", state.TxId)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return state, nil
}

// Exists checks if a key exists in the database
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

// Transaction allows execution of multiple operations in one transaction
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
		config.Info(ic.Logger,"Executing transaction with %d operations", len(tx.Ops))
		_, err := ic.Client.ExecAll(ic.Ctx, &schema.ExecAllRequest{
			Operations: tx.Ops,
		})
		
		if err != nil {
			return err
		}
		
		config.Info(ic.Logger,"Transaction with %d operations executed successfully", len(tx.Ops))
		return nil
	})
}



// Set adds a set operation to the transaction
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

// IsHealthy checks if the database connection is healthy
func IsHealthy(ic *config.ImmuClient) bool {
	if !ic.IsConnected {
		return false
	}
	
	// Try to get current state as a health check
	_, err := ic.Client.CurrentState(ic.Ctx)
	return err == nil
}

// Ping performs a health check on the database
func Ping(ic *config.ImmuClient) error {
	return withRetry(ic, "Ping", func() error {
		config.Info(ic.Logger,"Pinging ImmuDB")
		_, err := ic.Client.CurrentState(ic.Ctx)
		if err != nil {
			return err
		}
		config.Info(ic.Logger,"ImmuDB ping successful")
		return nil
	})
}