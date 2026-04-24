package database

import (
	"fmt"
	"os"
	"strconv"
)

// DBType represents the type of database backend
type DBType string

const (
	// DBTypeImmuDB represents ImmuDB database
	DBTypeImmuDB DBType = "immudb"

	// DBTypePostgreSQL represents PostgreSQL database (future)
	DBTypePostgreSQL DBType = "postgres"

	// DBTypeMongoDB represents MongoDB database (future)
	DBTypeMongoDB DBType = "mongodb"

	// DBTypeInMemory represents in-memory database (for testing)
	DBTypeInMemory DBType = "memory"
)

// Config holds database configuration
type Config struct {
	// Database type
	Type DBType

	// Connection details
	Host     string
	Port     int
	Database string
	Username string
	Password string

	// Storage path for embedded databases
	Path string

	// Connection pool settings
	MinConnections int
	MaxConnections int

	// ImmuDB-specific settings
	ImmuDBStateDir string
	ImmuDBCertPath string

	// PostgreSQL-specific settings
	PostgresSSLMode string

	// MongoDB-specific settings
	MongoDBReplicaSet string
}

// DefaultConfig returns default database configuration
// Uses in-memory storage with standard settings.
func DefaultConfig() *Config {
	return &Config{
		Type:           DBTypeInMemory,
		Host:           "localhost",
		Port:           3322,
		Database:       "contractsdb",
		Username:       "immudb",
		Password:       "immudb",
		MinConnections: 2,
		MaxConnections: 20,
		ImmuDBStateDir: "./.immudb_state",
	}
}

// LoadConfigFromEnv loads database configuration from environment variables
// Falls back to defaults if environment variables are not set
func LoadConfigFromEnv() *Config {
	config := DefaultConfig()

	// Database type
	if dbType := os.Getenv("DB_TYPE"); dbType != "" {
		config.Type = DBType(dbType)
	}

	// Connection details
	if host := os.Getenv("DB_HOST"); host != "" {
		config.Host = host
	}

	if portStr := os.Getenv("DB_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			config.Port = port
		}
	}

	if database := os.Getenv("DB_NAME"); database != "" {
		config.Database = database
	}

	if username := os.Getenv("DB_USER"); username != "" {
		config.Username = username
	}

	if password := os.Getenv("DB_PASS"); password != "" {
		config.Password = password
	}

	// Pool settings
	if minConn := os.Getenv("DB_MIN_CONNECTIONS"); minConn != "" {
		if min, err := strconv.Atoi(minConn); err == nil {
			config.MinConnections = min
		}
	}

	if maxConn := os.Getenv("DB_MAX_CONNECTIONS"); maxConn != "" {
		if max, err := strconv.Atoi(maxConn); err == nil {
			config.MaxConnections = max
		}
	}

	// ImmuDB-specific
	if stateDir := os.Getenv("IMMUDB_STATE_DIR"); stateDir != "" {
		config.ImmuDBStateDir = stateDir
	}

	// PostgreSQL-specific
	if sslMode := os.Getenv("POSTGRES_SSL_MODE"); sslMode != "" {
		config.PostgresSSLMode = sslMode
	}

	// MongoDB-specific
	if replicaSet := os.Getenv("MONGO_REPLICA_SET"); replicaSet != "" {
		config.MongoDBReplicaSet = replicaSet
	}

	return config
}

// Validate validates the database configuration
func (c *Config) Validate() error {
	if c.Type == "" {
		return fmt.Errorf("database type cannot be empty")
	}

	if c.Host == "" {
		return fmt.Errorf("database host cannot be empty")
	}

	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid database port: %d", c.Port)
	}

	if c.Type != DBTypeInMemory && c.Database == "" {
		return fmt.Errorf("database name cannot be empty for %s", c.Type)
	}

	if c.MinConnections < 0 {
		return fmt.Errorf("min connections cannot be negative")
	}

	if c.MaxConnections < c.MinConnections {
		return fmt.Errorf("max connections (%d) must be >= min connections (%d)", c.MaxConnections, c.MinConnections)
	}

	return nil
}

// String returns a string representation of the config (without password)
func (c *Config) String() string {
	return fmt.Sprintf("DB{type=%s, host=%s, port=%d, database=%s, user=%s}",
		c.Type, c.Host, c.Port, c.Database, c.Username)
}
