package DB_OPs

import (
    "crypto/sha256"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "sync"

    "github.com/cbergoon/merkletree"
    "github.com/dgraph-io/badger/v4"
)

// MerkleItem represents an item in the Merkle tree
type MerkleItem struct {
    Data []byte
}

// Hash function for Merkle tree
func (m MerkleItem) CalculateHash() ([]byte, error) {
    hash := sha256.Sum256(m.Data)
    return hash[:], nil
}

// Equals function for Merkle tree
func (m MerkleItem) Equals(other merkletree.Content) (bool, error) {
    return string(m.Data) == string(other.(MerkleItem).Data), nil
}

// DB is a wrapper around BadgerDB
type DB struct {
    instance *badger.DB
    path     string
    mutex    sync.RWMutex
}

// NewDB creates a new DB instance
func NewDB() *DB {
    return &DB{}
}

// InitDB initializes a new BadgerDB database
func (d *DB) InitDB(location, dbName string) error {
    // Create directories if they don't exist
    dbPath := filepath.Join(location, "DB", dbName)
    if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
        return fmt.Errorf("failed to create directory: %w", err)
    }

    // Open BadgerDB with proper options
    opts := badger.DefaultOptions(dbPath).
        WithLogger(nil).              // Disable logs
        WithSyncWrites(true).         // Ensure data durability
        WithCompactL0OnClose(true)    // Compact DB on close

    var err error
    d.instance, err = badger.Open(opts)
    if err != nil {
        return fmt.Errorf("failed to initialize database: %w", err)
    }
    
    d.path = dbPath
    return nil
}

// ConnectDB connects to an existing BadgerDB or creates a new one
func (d *DB) ConnectDB(location, dbName string) error {
    dbPath := filepath.Join(location, "DB", dbName)
    
    // Check if database directory exists
    if _, err := os.Stat(dbPath); os.IsNotExist(err) {
        // Create a new database
        return d.InitDB(location, dbName)
    }
    
    // Connect to existing database
    opts := badger.DefaultOptions(dbPath).
        WithLogger(nil).
        WithSyncWrites(true)
    
    var err error
    d.instance, err = badger.Open(opts)
    if err != nil {
        return fmt.Errorf("failed to connect to database: %w", err)
    }
    
    d.path = dbPath
    return nil
}

// Close closes the database
func (d *DB) Close() error {
    if d.instance == nil {
        return errors.New("database not initialized")
    }
    return d.instance.Close()
}

// AddData adds a key-value pair to the database
func (d *DB) AddData(key, value string) error {
    if d.instance == nil {
        return errors.New("database not initialized")
    }

    d.mutex.Lock()
    defer d.mutex.Unlock()

    return d.instance.Update(func(txn *badger.Txn) error {
        return txn.Set([]byte(key), []byte(value))
    })
}

// ReadData retrieves value for the given key
func (d *DB) ReadData(key string) (string, error) {
    if d.instance == nil {
        return "", errors.New("database not initialized")
    }

    d.mutex.RLock()
    defer d.mutex.RUnlock()

    var value string
    err := d.instance.View(func(txn *badger.Txn) error {
        item, err := txn.Get([]byte(key))
        if err != nil {
            return err
        }

        return item.Value(func(val []byte) error {
            value = string(val)
            return nil
        })
    })

    if err != nil {
        return "", fmt.Errorf("failed to read data: %w", err)
    }
    return value, nil
}

// ComputeMerkleRoot calculates the Merkle root of all key-value pairs
func (d *DB) ComputeMerkleRoot() (string, error) {
    if d.instance == nil {
        return "", errors.New("database not initialized")
    }

    d.mutex.RLock()
    defer d.mutex.RUnlock()

    var items []merkletree.Content

    // Collect all key-value pairs
    err := d.instance.View(func(txn *badger.Txn) error {
        opts := badger.DefaultIteratorOptions
        it := txn.NewIterator(opts)
        defer it.Close()

        for it.Rewind(); it.Valid(); it.Next() {
            item := it.Item()
            err := item.Value(func(val []byte) error {
                // Combine key and value to create unique content
                data := append(item.Key(), val...)
                items = append(items, MerkleItem{Data: data})
                return nil
            })
            if err != nil {
                return err
            }
        }
        return nil
    })

    if err != nil {
        return "", fmt.Errorf("failed to read database: %w", err)
    }

    if len(items) == 0 {
        return "", errors.New("database is empty")
    }

    // Create and validate the Merkle tree
    tree, err := merkletree.NewTree(items)
    if err != nil {
        return "", fmt.Errorf("failed to create Merkle tree: %w", err)
    }

    return fmt.Sprintf("%x", tree.MerkleRoot()), nil
}

// DeleteData deletes entries by key or value
func (d *DB) DeleteData(keyOrValue string, flag string) error {
    if d.instance == nil {
        return errors.New("database not initialized")
    }

    d.mutex.Lock()
    defer d.mutex.Unlock()

    if flag != "key" && flag != "value" {
        return errors.New("invalid flag, must be 'key' or 'value'")
    }

    return d.instance.Update(func(txn *badger.Txn) error {
        if flag == "key" {
            // Delete by key
            return txn.Delete([]byte(keyOrValue))
        } else {
            // Delete by value (search for matching values)
            opts := badger.DefaultIteratorOptions
            it := txn.NewIterator(opts)
            defer it.Close()

            // Keep track of keys to delete
            keysToDelete := [][]byte{}

            // First find all matching entries
            for it.Rewind(); it.Valid(); it.Next() {
                item := it.Item()
                err := item.Value(func(val []byte) error {
                    if string(val) == keyOrValue {
                        // Make a copy of the key since item.Key() is only valid within this transaction
                        keyCopy := make([]byte, len(item.Key()))
                        copy(keyCopy, item.Key())
                        keysToDelete = append(keysToDelete, keyCopy)
                    }
                    return nil
                })
                if err != nil {
                    return err
                }
            }

            // Delete all found keys
            for _, key := range keysToDelete {
                if err := txn.Delete(key); err != nil {
                    return err
                }
            }

            if len(keysToDelete) == 0 {
                return fmt.Errorf("no entries found with value: %s", keyOrValue)
            }
            return nil
        }
    })
}

// ReadAll retrieves all key-value pairs from the database
func (d *DB) ReadAll() (map[string]string, error) {
    if d.instance == nil {
        return nil, errors.New("database not initialized")
    }

    d.mutex.RLock()
    defer d.mutex.RUnlock()

    data := make(map[string]string)

    err := d.instance.View(func(txn *badger.Txn) error {
        opts := badger.DefaultIteratorOptions
        it := txn.NewIterator(opts)
        defer it.Close()

        for it.Rewind(); it.Valid(); it.Next() {
            item := it.Item()
            key := string(item.Key())

            err := item.Value(func(val []byte) error {
                data[key] = string(val)
                return nil
            })
            if err != nil {
                return err
            }
        }
        return nil
    })

    if err != nil {
        return nil, fmt.Errorf("failed to read all data: %w", err)
    }
    return data, nil
}

// RunGC manually runs garbage collection on the database
func (d *DB) RunGC() error {
    if d.instance == nil {
        return errors.New("database not initialized")
    }
    
    return d.instance.RunValueLogGC(0.5) // 0.5 is the discardRatio
}

// GetStats returns database statistics
func (d *DB) GetStats() map[string]string {
    stats := make(map[string]string)
    
    if d.instance == nil {
        stats["error"] = "Database not initialized"
        return stats
    }

    lsmSize, vlogSize := d.instance.Size()
    stats["lsm_size"] = fmt.Sprintf("%d", lsmSize)
    stats["vlog_size"] = fmt.Sprintf("%d", vlogSize)
    stats["total_size"] = fmt.Sprintf("%d", lsmSize+vlogSize)
    stats["db_path"] = d.path
    
    return stats
}