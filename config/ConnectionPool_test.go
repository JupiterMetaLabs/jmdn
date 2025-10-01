package config

import (
	"testing"
	"time"
)

// ConnectionCreator interface for testing
type ConnectionCreator interface {
	createConnection() (*PooledConnection, error)
}

// TestConnectionPool wraps ConnectionPool to expose createConnection for testing
type TestConnectionPool struct {
	*ConnectionPool
	createConnectionFunc func() (*PooledConnection, error)
}

func (tcp *TestConnectionPool) createConnection() (*PooledConnection, error) {
	if tcp.createConnectionFunc != nil {
		return tcp.createConnectionFunc()
	}
	return tcp.ConnectionPool.createConnection()
}

// mock createConnection to avoid real DB calls
func mockCreateConnection() *PooledConnection {
	now := time.Now()
	return &PooledConnection{
		Client:      nil, // could inject a fake client
		Token:       "test-token",
		TokenExpiry: now.Add(1 * time.Hour),
		Database:    "defaultdb",
		CreatedAt:   now,
		LastUsed:    now,
		InUse:       false,
	}
}

func TestConnectionPool_Get_NewAndReuse(t *testing.T) {
	// setup fake logger
	logger, err := NewAsyncLogger()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	// setup pool config
	poolCfg := DefaultConnectionPoolConfig()
	basePool := &ConnectionPool{
		Config:      poolCfg,
		Connections: []*PooledConnection{},
		Logger:      logger,
		Address:     "localhost",
		Port:        3322,
		Database:    "defaultdb",
		Username:    DBUsername,
		Password:    DBPassword,
	}

	// Create test wrapper with mock
	pool := &TestConnectionPool{
		ConnectionPool: basePool,
		createConnectionFunc: func() (*PooledConnection, error) {
			return mockCreateConnection(), nil
		},
	}

	// 1. First call should create a new connection
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn1 == nil {
		t.Fatal("expected a connection, got nil")
	}

	// Return connection to pool
	pool.Put(conn1)

	// 2. Second call should reuse the same connection
	conn2, err := pool.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn1 != conn2 {
		t.Error("expected to reuse the same connection")
	}
}

func TestConnectionPool_Get_WithRealDB(t *testing.T) {
	// Test with real database connection using ../.immudb_state
	logger, err := NewAsyncLogger()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	// setup pool config
	poolCfg := DefaultConnectionPoolConfig()
	poolCfg.MaxConnections = 2 // Limit for testing

	// Use parent directory for .immudb_state
	poolingConfig := &PoolingConfig{
		DBAddress:  "localhost",
		DBPort:     3322,
		DBName:     "defaultdb",
		DBUsername: DBUsername,
		DBPassword: DBPassword,
	}

	// Create a real connection pool
	pool := NewConnectionPool(poolCfg, logger, poolingConfig)
	defer pool.Close()

	// Test getting a connection
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}
	if conn1 == nil {
		t.Fatal("expected a connection, got nil")
	}

	t.Logf("Got connection: Token=%s, Database=%s", conn1.Token, conn1.Database)

	// Return connection to pool
	pool.Put(conn1)

	// Test getting another connection (should reuse)
	conn2, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get second connection: %v", err)
	}
	if conn2 == nil {
		t.Fatal("expected a second connection, got nil")
	}

	t.Logf("Got second connection: Token=%s, Database=%s", conn2.Token, conn2.Database)
}

func TestConnectionPool_Get_MaxConnections(t *testing.T) {
	// Test max connections limit with real database
	logger, err := NewAsyncLogger()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	poolCfg := DefaultConnectionPoolConfig()
	poolCfg.MaxConnections = 1 // only allow 1 connection

	// Use parent directory for .immudb_state
	poolingConfig := &PoolingConfig{
		DBAddress:  "localhost",
		DBPort:     3322,
		DBName:     "defaultdb",
		DBUsername: "immudb",
		DBPassword: "immudb",
	}

	// Create a real connection pool
	pool := NewConnectionPool(poolCfg, logger, poolingConfig)
	defer pool.Close()

	// first connection
	t.Log("Getting first connection...")
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("Got first connection: Token=%s", conn1.Token)

	// second connection should fail because conn1 is still InUse
	t.Log("Trying to get second connection (should fail due to max connections)...")
	_, err = pool.Get()
	if err == nil {
		t.Fatal("expected error due to max connections, got nil")
	}
	t.Logf("✅ Correctly got error for max connections: %v", err)

	// return connection and try again
	t.Log("Returning first connection to pool...")
	pool.Put(conn1)

	t.Log("Getting connection again (should reuse)...")
	conn2, err := pool.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn2 != conn1 {
		t.Error("expected to reuse the same connection after Put")
	} else {
		t.Log("✅ Connection was successfully reused!")
	}
}
