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
	"sync"
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

// AsyncLogger provides asynchronous file logging
type AsyncLogger struct {
	logger  *log.Logger
	logChan chan string
	wg      sync.WaitGroup
	file    *os.File
}

// NewAsyncLogger creates a new async logger that writes to logs/ImmuDB.log
func NewAsyncLogger() (*AsyncLogger, error) {
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
	asyncLogger := &AsyncLogger{
		logger:  logger,
		logChan: make(chan string, 1000), // Buffer up to 1000 log messages
		file:    file,
	}
	
	// Start background worker
	asyncLogger.wg.Add(1)
	go asyncLogger.processLogs()
	
	return asyncLogger, nil
}

// processLogs continuously processes log messages from the channel
func (al *AsyncLogger) processLogs() {
	defer al.wg.Done()
	
	for msg := range al.logChan {
		al.logger.Println(msg)
	}
}

// log sends a log message to the channel
func (al *AsyncLogger) log(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(level+": "+format, args...)
	
	// Non-blocking send to channel with timeout
	select {
	case al.logChan <- msg:
		// Message sent successfully
	case <-time.After(time.Millisecond * 10):
		// Channel is full, drop the message
	}
}

// Info logs an info message
func (al *AsyncLogger) Info(format string, args ...interface{}) {
	al.log("INFO", format, args...)
}

// Warning logs a warning message
func (al *AsyncLogger) Warning(format string, args ...interface{}) {
	al.log("WARNING", format, args...)
}

// Error logs an error message
func (al *AsyncLogger) Error(format string, args ...interface{}) {
	al.log("ERROR", format, args...)
}

// Close closes the logger
func (al *AsyncLogger) Close() error {
	// Close channel and wait for worker to finish
	close(al.logChan)
	al.wg.Wait()
	
	// Close file
	return al.file.Close()
}

// ImmuClient provides a simplified interface for ImmuDB operations
type ImmuClient struct {
	client      client.ImmuClient
	ctx         context.Context
	cancel      context.CancelFunc
	baseCtx     context.Context
	token       string
	retryLimit  int
	isConnected bool
	logger      *AsyncLogger
}

// ImmuClientOption defines functional options for ImmuClient configuration
type ImmuClientOption func(*ImmuClient)

// WithLogger sets a custom logger for the ImmuClient
func WithLogger(logger *AsyncLogger) ImmuClientOption {
	return func(ic *ImmuClient) {
		ic.logger = logger
	}
}

// WithRetryLimit sets the maximum number of retry attempts
func WithRetryLimit(limit int) ImmuClientOption {
	return func(ic *ImmuClient) {
		ic.retryLimit = limit
	}
}

// New creates and returns a connected ImmuClient
func New(options ...ImmuClientOption) (*ImmuClient, error) {
	// Create a default async logger
	defaultLogger, err := NewAsyncLogger()
	if err != nil {
		return nil, fmt.Errorf("failed to create default logger: %w", err)
	}
	
	// Create a default client
	ic := &ImmuClient{
		baseCtx:     context.Background(),
		retryLimit:  3,
		logger:      defaultLogger,
		isConnected: false,
	}

	// Apply custom options
	for _, option := range options {
		option(ic)
	}

	// Establish connection
	err = ic.connect()
	if err != nil {
		ic.logger.Close()
		return nil, err
	}

	return ic, nil
}

// connect establishes a connection to ImmuDB
func (ic *ImmuClient) connect() error {
	ic.logger.Info("Connecting to ImmuDB at %s:%d", config.DBAddress, config.DBPort)
	
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ic.baseCtx, config.RequestTimeout)
	
	// Login to immudb
	ic.logger.Info("Authenticating with ImmuDB")
	lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("login failed: %w", err)
	}

	// Store token for reconnection if needed
	ic.token = lr.Token
	
	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)
	
	// Select database
	ic.logger.Info("Selecting database: %s", config.DBName)
	_, err = c.UseDatabase(ctx, &schema.Database{DatabaseName: config.DBName})
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("failed to use database %s: %w", config.DBName, err)
	}

	ic.client = c
	ic.ctx = ctx
	ic.cancel = cancel
	ic.isConnected = true
	ic.logger.Info("Successfully connected to ImmuDB")

	return nil
}

// reconnect attempts to reestablish a lost connection
func (ic *ImmuClient) reconnect() error {
	ic.logger.Warning("Attempting to reconnect to ImmuDB")
	
	// Clean up existing connection if any
	if ic.cancel != nil {
		ic.cancel()
	}
	
	if ic.client != nil {
		ic.client.Disconnect()
	}
	
	ic.isConnected = false
	
	// Attempt to connect again
	return ic.connect()
}

// withRetry executes the given operation with retry logic
func (ic *ImmuClient) withRetry(operation string, fn func() error) error {
	var err error
	
	for attempt := 0; attempt <= ic.retryLimit; attempt++ {
		// Check connection status first
		if !ic.isConnected {
			ic.logger.Warning("Connection lost, attempting to reconnect before %s operation", operation)
			if err = ic.reconnect(); err != nil {
				ic.logger.Error("Reconnection attempt %d/%d failed: %v", attempt+1, ic.retryLimit+1, err)
				if attempt == ic.retryLimit {
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
			ic.logger.Warning("%s operation failed due to connection issue (attempt %d/%d): %v", 
				operation, attempt+1, ic.retryLimit+1, err)
			ic.isConnected = false
			if attempt < ic.retryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}
		
		// Non-connection error or final attempt
		ic.logger.Error("%s operation failed (attempt %d/%d): %v", 
			operation, attempt+1, ic.retryLimit+1, err)
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
func (ic *ImmuClient) Create(key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	
	if value == nil {
		return ErrNilValue
	}

	return ic.withRetry("Create", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		ic.logger.Info("Creating key: %s", key)
		// Store the key-value pair
		_, err = ic.client.Set(ic.ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}
		
		ic.logger.Info("Successfully created key: %s", key)
		return nil
	})
}

// Read retrieves a value by key
func (ic *ImmuClient) Read(key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	
	err := ic.withRetry("Read", func() error {
		ic.logger.Info("Reading key: %s", key)
		entry, err := ic.client.Get(ic.ctx, []byte(key))
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
func (ic *ImmuClient) ReadJSON(key string, dest interface{}) error {
	data, err := ic.Read(key)
	if err != nil {
		return err
	}

	ic.logger.Info("Unmarshaling JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value
func (ic *ImmuClient) Update(key string, value interface{}) error {
	// In ImmuDB, update is the same as create since it's an immutable database
	// We simply write the new value with the same key
	ic.logger.Info("Updating key: %s", key)
	return ic.Create(key, value)
}

// GetKeys retrieves keys with a specified prefix
func (ic *ImmuClient) GetKeys(prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var keys []string
	
	err := ic.withRetry("GetKeys", func() error {
		ic.logger.Info("Scanning keys with prefix: %s (limit: %d)", prefix, limit)
		scanReq := &schema.ScanRequest{
			Prefix: []byte(prefix),
			Limit:  uint64(limit),
		}

		scanResult, err := ic.client.Scan(ic.ctx, scanReq)
		if err != nil {
			return err
		}

		keys = make([]string, len(scanResult.Entries))
		for i, entry := range scanResult.Entries {
			keys[i] = string(entry.Key)
		}
		
		ic.logger.Info("Found %d keys with prefix: %s", len(keys), prefix)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return keys, nil
}

// BatchCreate stores multiple key-value pairs in a single transaction
func (ic *ImmuClient) BatchCreate(entries map[string]interface{}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}

	return ic.withRetry("BatchCreate", func() error {
		ic.logger.Info("Creating batch of %d entries", len(entries))
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
		_, err := ic.client.ExecAll(ic.ctx, &schema.ExecAllRequest{
			Operations: ops,
		})
		
		if err != nil {
			return err
		}
		
		ic.logger.Info("Successfully created batch of %d entries", len(entries))
		return nil
	})
}

// Close closes the ImmuDB client connection
func (ic *ImmuClient) Close() error {
	ic.logger.Info("Closing ImmuDB connection")
	
	if ic.cancel != nil {
		ic.cancel()
	}
	
	if ic.client != nil {
		err := ic.client.Disconnect()
		if err != nil {
			ic.logger.Error("Error disconnecting from ImmuDB: %v", err)
			return fmt.Errorf("error disconnecting from ImmuDB: %w", err)
		}
	}
	
	ic.isConnected = false
	ic.logger.Info("ImmuDB connection closed successfully")
	
	// Close the logger
	err := ic.logger.Close()
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
func (ic *ImmuClient) GetMerkleRoot() ([]byte, error) {
	var merkleRoot []byte
	
	err := ic.withRetry("GetMerkleRoot", func() error {
		ic.logger.Info("Getting current Merkle root")
		// Get current state from server
		state, err := ic.client.CurrentState(ic.ctx)
		if err != nil {
			return err
		}

		// Extract the Merkle root (txHash)
		merkleRoot = state.TxHash
		
		ic.logger.Info("Database state: TxId=%d, TxHash=%x", state.TxId, state.TxHash)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return merkleRoot, nil
}

// SafeCreate stores a value with the given key and verifies the operation
func (ic *ImmuClient) SafeCreate(key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	
	if value == nil {
		return ErrNilValue
	}

	return ic.withRetry("SafeCreate", func() error {
		// Convert value to bytes
		valueBytes, err := toBytes(value)
		if err != nil {
			return err
		}

		ic.logger.Info("Creating verified key: %s", key)
		// Store the key-value pair with verification
		verifiedTx, err := ic.client.VerifiedSet(ic.ctx, []byte(key), valueBytes)
		if err != nil {
			return err
		}

		ic.logger.Info("Transaction verified: tx=%d, verified=%v", 
			verifiedTx.Id, verifiedTx)
		return nil
	})
}

// SafeRead retrieves a value by key with cryptographic verification
func (ic *ImmuClient) SafeRead(key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	var entryValue []byte
	
	err := ic.withRetry("SafeRead", func() error {
		ic.logger.Info("Reading verified key: %s", key)
		entry, err := ic.client.VerifiedGet(ic.ctx, []byte(key))
		if err != nil {
			if err.Error() == "key not found" {
				return ErrNotFound
			}
			return err
		}
		
		ic.logger.Info("Value verified: tx=%d, verified=%v", 
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
func (ic *ImmuClient) SafeReadJSON(key string, dest interface{}) error {
	data, err := ic.SafeRead(key)
	if err != nil {
		return err
	}

	ic.logger.Info("Unmarshaling verified JSON data for key: %s", key)
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// GetHistory retrieves the history of values for a key
func (ic *ImmuClient) GetHistory(key string, limit int) ([]*schema.Entry, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	var entries []*schema.Entry
	
	err := ic.withRetry("GetHistory", func() error {
		ic.logger.Info("Getting history for key: %s (limit: %d)", key, limit)
		historyReq := &schema.HistoryRequest{
			Key:   []byte(key),
			Limit: int32(limit),
		}

		historyResp, err := ic.client.History(ic.ctx, historyReq)
		if err != nil {
			return err
		}
		
		entries = historyResp.Entries
		ic.logger.Info("Found %d historical entries for key: %s", len(entries), key)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return entries, nil
}

// BlockHasher for generating block hashes
type BlockHasher struct{}

// NewBlockHasher creates a new BlockHasher
func NewBlockHasher() *BlockHasher {
	return &BlockHasher{}
}

// HashBlock generates a hash for a block using the nonce, sender, and timestamp
func (h *BlockHasher) HashBlock(nonce, sender string, timestamp int64) string {
	// Create a deterministic string from the block components
	data := fmt.Sprintf("%s-%s-%d", nonce, sender, timestamp)
	
	// Hash the data
	hash := sha256.Sum256([]byte(data))
	
	// Return hex-encoded hash, truncated for readability
	return hex.EncodeToString(hash[:])[:16]
}

// GetDatabaseState returns the current state of the database
func (ic *ImmuClient) GetDatabaseState() (*schema.ImmutableState, error) {
	var state *schema.ImmutableState
	
	err := ic.withRetry("GetDatabaseState", func() error {
		ic.logger.Info("Getting current database state")
		// Get current state from server
		dbState, err := ic.client.CurrentState(ic.ctx)
		if err != nil {
			return err
		}
		
		state = dbState
		ic.logger.Info("Database state retrieved: TxId=%d", state.TxId)
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	return state, nil
}

// Exists checks if a key exists in the database
func (ic *ImmuClient) Exists(key string) (bool, error) {
	if key == "" {
		return false, ErrEmptyKey
	}
	
	_, err := ic.Read(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	
	return true, nil
}

// Transaction allows execution of multiple operations in one transaction
func (ic *ImmuClient) Transaction(fn func(tx *ImmuTransaction) error) error {
	// Create transaction object
	tx := &ImmuTransaction{
		client: ic,
		ops:    make([]*schema.Op, 0),
	}
	
	// Execute transaction function
	if err := fn(tx); err != nil {
		return err
	}
	
	// If no operations, just return
	if len(tx.ops) == 0 {
		return nil
	}
	
	// Execute transaction
	return ic.withRetry("Transaction", func() error {
		ic.logger.Info("Executing transaction with %d operations", len(tx.ops))
		_, err := ic.client.ExecAll(ic.ctx, &schema.ExecAllRequest{
			Operations: tx.ops,
		})
		
		if err != nil {
			return err
		}
		
		ic.logger.Info("Transaction with %d operations executed successfully", len(tx.ops))
		return nil
	})
}

// ImmuTransaction represents a transaction in ImmuDB
type ImmuTransaction struct {
	client *ImmuClient
	ops    []*schema.Op
}

// Set adds a set operation to the transaction
func (tx *ImmuTransaction) Set(key string, value interface{}) error {
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
	
	tx.ops = append(tx.ops, &schema.Op{
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
func (ic *ImmuClient) IsHealthy() bool {
	if !ic.isConnected {
		return false
	}
	
	// Try to get current state as a health check
	_, err := ic.client.CurrentState(ic.ctx)
	return err == nil
}

// Ping performs a health check on the database
func (ic *ImmuClient) Ping() error {
	return ic.withRetry("Ping", func() error {
		ic.logger.Info("Pinging ImmuDB")
		_, err := ic.client.CurrentState(ic.ctx)
		if err != nil {
			return err
		}
		ic.logger.Info("ImmuDB ping successful")
		return nil
	})
}