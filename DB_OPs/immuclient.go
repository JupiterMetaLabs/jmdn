package DB_OPs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gossipnode/config"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"google.golang.org/grpc/metadata"
)

// ImmuClient provides a simplified interface for ImmuDB operations
type ImmuClient struct {
	client client.ImmuClient
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates and returns a connected ImmuClient with default options
func New() (*ImmuClient, error) {
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
	
	// Login to immudb
	lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
	if err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("login failed: %w", err)
	}

	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)
	
	// Select database
	_, err = c.UseDatabase(ctx, &schema.Database{DatabaseName: config.DBName})
	if err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("failed to use database %s: %w", config.DBName, err)
	}

	return &ImmuClient{
		client: c,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Create stores a value with the given key
func (ic *ImmuClient) Create(key string, value interface{}) error {
	if key == "" {
		return errors.New("key cannot be empty")
	}

	// Convert value to bytes
	valueBytes, err := toBytes(value)
	if err != nil {
		return err
	}

	// Store the key-value pair
	_, err = ic.client.Set(ic.ctx, []byte(key), valueBytes)
	if err != nil {
		return fmt.Errorf("failed to store data: %w", err)
	}

	return nil
}

// Read retrieves a value by key
func (ic *ImmuClient) Read(key string) ([]byte, error) {
	if key == "" {
		return nil, errors.New("key cannot be empty")
	}

	entry, err := ic.client.Get(ic.ctx, []byte(key))
	if err != nil {
		return nil, fmt.Errorf("failed to read key %s: %w", key, err)
	}

	return entry.Value, nil
}

// ReadJSON retrieves a value by key and unmarshals it into dest
func (ic *ImmuClient) ReadJSON(key string, dest interface{}) error {
	data, err := ic.Read(key)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value
func (ic *ImmuClient) Update(key string, value interface{}) error {
	// In ImmuDB, update is the same as create since it's an immutable database
	// We simply write the new value with the same key
	return ic.Create(key, value)
}

// GetKeys retrieves keys with a specified prefix
func (ic *ImmuClient) GetKeys(prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = config.DefaultScanLimit
	}

	scanReq := &schema.ScanRequest{
		Prefix: []byte(prefix),
		Limit:  uint64(limit),
	}

	scanResult, err := ic.client.Scan(ic.ctx, scanReq)
	if err != nil {
		return nil, fmt.Errorf("failed to scan keys: %w", err)
	}

	keys := make([]string, len(scanResult.Entries))
	for i, entry := range scanResult.Entries {
		keys[i] = string(entry.Key)
	}

	return keys, nil
}

// BatchCreate stores multiple key-value pairs in a single transaction
func (ic *ImmuClient) BatchCreate(entries map[string]interface{}) error {
	if len(entries) == 0 {
		return errors.New("entries map cannot be empty")
	}

	ops := make([]*schema.Op, 0, len(entries))
	
	for key, value := range entries {
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
		return fmt.Errorf("batch operation failed: %w", err)
	}

	return nil
}

// Close closes the ImmuDB client connection
func (ic *ImmuClient) Close() error {
	if ic.cancel != nil {
		ic.cancel()
	}
	return ic.client.Disconnect()
}

// Helper function to convert various value types to bytes
func toBytes(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
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
    // Get current state from server
    state, err := ic.client.CurrentState(ic.ctx)
    if err != nil {
        return nil, fmt.Errorf("failed to get current state: %w", err)
    }

    // Extract the Merkle root (txHash)
    merkleRoot := state.TxHash

    // Print detailed information for debugging
    fmt.Printf("Database state: TxId=%d, TxHash=%x\n", state.TxId, state.TxHash)
    
    return merkleRoot, nil
}

// SafeCreate stores a value with the given key and verifies the operation
func (ic *ImmuClient) SafeCreate(key string, value interface{}) error {
    if key == "" {
        return errors.New("key cannot be empty")
    }

    // Convert value to bytes
    valueBytes, err := toBytes(value)
    if err != nil {
        return err
    }

    // Store the key-value pair with verification
    verifiedTx, err := ic.client.VerifiedSet(ic.ctx, []byte(key), valueBytes)
    if err != nil {
        return fmt.Errorf("failed to store data: %w", err)
    }

    fmt.Printf("Transaction verified: tx=%d, verified=%t\n", 
        verifiedTx.Id, verifiedTx)

    return nil
}

// SafeRead retrieves a value by key with cryptographic verification
func (ic *ImmuClient) SafeRead(key string) ([]byte, error) {
    if key == "" {
        return nil, errors.New("key cannot be empty")
    }

    entry, err := ic.client.VerifiedGet(ic.ctx, []byte(key))
    if err != nil {
        return nil, fmt.Errorf("failed to read key %s: %w", key, err)
    }

    fmt.Printf("Value verified: tx=%d, verified=%t\n", 
        entry.Tx, entry)

    return entry.Value, nil
}

// SafeReadJSON retrieves a verified value by key and unmarshals it into dest
func (ic *ImmuClient) SafeReadJSON(key string, dest interface{}) error {
    data, err := ic.SafeRead(key)
    if err != nil {
        return err
    }

    if err := json.Unmarshal(data, dest); err != nil {
        return fmt.Errorf("failed to unmarshal data: %w", err)
    }

    return nil
}

// GetHistory retrieves the history of values for a key
func (ic *ImmuClient) GetHistory(key string, limit int) ([]*schema.Entry, error) {
    if key == "" {
        return nil, errors.New("key cannot be empty")
    }
    
    if limit <= 0 {
        limit = config.DefaultScanLimit
    }

    historyReq := &schema.HistoryRequest{
        Key:   []byte(key),
		Limit: int32(limit),
    }

    historyResp, err := ic.client.History(ic.ctx, historyReq)
    if err != nil {
        return nil, fmt.Errorf("failed to get history for key %s: %w", key, err)
    }

    return historyResp.Entries, nil
}
