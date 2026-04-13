package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gossipnode/config"
	"gossipnode/logging"
)

var (
	// Global registry of connection pools by database name
	contractsDBPools = make(map[string]*config.ConnectionPool)
	poolMutex        sync.RWMutex
)

// GetOrCreateContractsDBPool gets or creates an ImmuDB connection pool for contractsdb
func GetOrCreateContractsDBPool(cfg *Config) (*config.PooledConnection, error) {
	if cfg.Type != DBTypeImmuDB {
		return nil, fmt.Errorf("config is not for ImmuDB: %s", cfg.Type)
	}

	// Get or create pool
	pool, err := getOrCreateContractsPool(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create pool: %w", err)
	}

	// Get connection from pool
	conn, err := pool.Get(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get connection from pool: %w", err)
	}

	return conn, nil
}

// getOrCreateContractsPool gets or creates a connection pool (internal helper)
func getOrCreateContractsPool(cfg *Config) (*config.ConnectionPool, error) {
	poolKey := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)

	// Try to get existing pool (read lock)
	poolMutex.RLock()
	if pool, exists := contractsDBPools[poolKey]; exists {
		poolMutex.RUnlock()
		return pool, nil
	}
	poolMutex.RUnlock()

	// Create new pool (write lock)
	poolMutex.Lock()
	defer poolMutex.Unlock()

	// Double-check after acquiring write lock
	if pool, exists := contractsDBPools[poolKey]; exists {
		return pool, nil
	}

	// Create connection pool configuration
	poolingConfig := &config.PoolingConfig{
		DBAddress:  cfg.Host,
		DBPort:     cfg.Port,
		DBName:     cfg.Database,
		DBUsername: cfg.Username,
		DBPassword: cfg.Password,
	}

	// Create async logger for the pool
	asyncLog := logging.NewAsyncLogger()
	logger, err := asyncLog.NamedLogger("ContractsDB", "contracts_db.log")
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Create pool config
	poolConfig := &config.ConnectionPoolConfig{
		MinConnections:     cfg.MinConnections,
		MaxConnections:     cfg.MaxConnections,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,
		TokenMaxLifetime:   24 * time.Hour,
		TokenRefreshBuffer: 5 * time.Minute,
	}

	// Create the pool
	pool := config.NewConnectionPool(context.Background(), poolConfig, logger.NamedLogger, poolingConfig)

	// Store in registry
	contractsDBPools[poolKey] = pool

	return pool, nil
}

// ReturnContractsDBConnection returns a connection to its pool
func ReturnContractsDBConnection(conn *config.PooledConnection) {
	if conn == nil {
		return
	}

	// Get the pool that owns this connection
	poolMutex.RLock()
	defer poolMutex.RUnlock()

	// Find the pool by database name
	for _, pool := range contractsDBPools {
		// Return connection to pool
		pool.Put(context.Background(), conn)
		return
	}
}

// CloseAllPools closes all connection pools
func CloseAllPools() error {
	poolMutex.Lock()
	defer poolMutex.Unlock()

	var errors []error

	for key, pool := range contractsDBPools {
		pool.Close(context.Background())
		delete(contractsDBPools, key)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to close %d pools", len(errors))
	}

	return nil
}

// GetPoolStats returns statistics about connection pools
func GetPoolStats() map[string]PoolStats {
	poolMutex.RLock()
	defer poolMutex.RUnlock()

	stats := make(map[string]PoolStats)

	for key, pool := range contractsDBPools {
		pool.Mutex.RLock()
		stats[key] = PoolStats{
			TotalConnections:  len(pool.Connections),
			ActiveConnections: countActiveConnections(pool),
			IdleConnections:   len(pool.Connections) - countActiveConnections(pool),
		}
		pool.Mutex.RUnlock()
	}

	return stats
}

// PoolStats contains statistics about a connection pool
type PoolStats struct {
	TotalConnections  int
	ActiveConnections int
	IdleConnections   int
}

// countActiveConnections counts how many connections are in use
func countActiveConnections(pool *config.ConnectionPool) int {
	count := 0
	for _, conn := range pool.Connections {
		if conn.InUse {
			count++
		}
	}
	return count
}

// EnsureDatabaseExists checks if a database exists and creates it if it doesn't
// This is useful for initializing contractsdb on first run
func EnsureDatabaseExists(ctx context.Context, cfg *Config) error {
	if cfg.Type != DBTypeImmuDB {
		// Only ImmuDB requires database creation
		return nil
	}

	// Get a connection
	conn, err := GetOrCreateContractsDBPool(cfg)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer ReturnContractsDBConnection(conn)

	// Try to select the database
	// If it fails with "database does not exist", we'll create it
	// This logic will be implemented when we have the actual ImmuDB client

	// For now, just return nil
	// The database should be created manually before starting the service
	return nil
}

// WaitForDatabase waits for the database to become available
// Useful during startup when database might not be ready yet
func WaitForDatabase(ctx context.Context, cfg *Config, maxRetries int) error {
	for i := 0; i < maxRetries; i++ {
		_, err := GetOrCreateContractsDBPool(cfg)
		if err == nil {
			return nil
		}

		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second * time.Duration(i+1)):
			// Exponential backoff
		}
	}

	return fmt.Errorf("database not available after %d retries", maxRetries)
}
