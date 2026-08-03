package DB_OPs

import (
	"errors"
	"time"
)

// Accounts DB key constants

const (
	Prefix    = "address:"
	DIDPrefix = "did:"
)

// Main DB key constants
const (
	DEFAULT_PREFIX_TX      = "tx:"
	PREFIX_BLOCK           = "block:"
	PREFIX_BLOCK_HASH      = "block:hash:"
	DEFAULT_PREFIX_RECEIPT = "receipt:"
)

// LOKI_URL will be set conditionally based on whether Loki is enabled
var LOKI_URL string

// Logging constants
const (
	LOG_FILE        = "ThebeDB.log"
	LOG_DIR         = "logs"
	LOKI_BATCH_SIZE = 128 * 1024
	LOKI_BATCH_WAIT = 1 * time.Second
	LOKI_TIMEOUT    = 5 * time.Second
	KEEP_LOGS       = true
	TOPIC           = "ThebeDB_DBOps"
)

// Custom errors
var (
	ErrEmptyKey        = errors.New("key cannot be empty")
	ErrEmptyBatch      = errors.New("entries map cannot be empty")
	ErrNilValue        = errors.New("value cannot be nil")
	ErrNotFound        = errors.New("key not found")
	ErrConnectionLost  = errors.New("database connection lost")
	ErrPoolClosed      = errors.New("connection pool is closed")
	ErrTokenExpired    = errors.New("authentication token expired")
	ErrNoAvailableConn = errors.New("no available connections in pool")
)
