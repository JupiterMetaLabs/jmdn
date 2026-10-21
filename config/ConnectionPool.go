package config

import (
	"io"
	"time"
)

// ConnectionPoolConfig holds the legacy connection-pool settings. ThebeDB is
// an embedded store reached through a process-wide handle, so nothing pools
// connections any more; the type remains for the DB_OPs compatibility shims
// that still accept it.
type ConnectionPoolConfig struct {
	MinConnections     int           // Minimum number of connections to maintain
	MaxConnections     int           // Maximum number of connections allowed
	ConnectionTimeout  time.Duration // Timeout for establishing new connections
	IdleTimeout        time.Duration // Time after which idle connections are closed
	MaxLifetime        time.Duration // Maximum lifetime of a connection
	TokenMaxLifetime   time.Duration // Maximum lifetime of a token
	TokenRefreshBuffer time.Duration // How early to refresh tokens before expiry
}

// DefaultConnectionPoolConfig returns default configuration
func DefaultConnectionPoolConfig() *ConnectionPoolConfig {
	return &ConnectionPoolConfig{
		MinConnections:     2,
		MaxConnections:     30,
		ConnectionTimeout:  30 * time.Second,
		IdleTimeout:        5 * time.Minute,
		MaxLifetime:        30 * time.Minute,
		TokenMaxLifetime:   24 * time.Hour, // legacy default token lifetime (unused on the Thebe path)
		TokenRefreshBuffer: 5 * time.Minute,
	}
}

// PooledConnection is the connection parameter many DB_OPs helpers still
// accept. Callers pass nil; getHandle resolves Handle first and otherwise
// falls back to the process-wide ThebeHandle.
type PooledConnection struct {
	Handle    io.Closer
	Database  string
	CreatedAt time.Time
	LastUsed  time.Time
	InUse     bool
}
