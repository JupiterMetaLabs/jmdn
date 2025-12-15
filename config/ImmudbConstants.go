package config

import (
	"context"
	"time"

	"gossipnode/logging"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
)

const (
	// Database connection settings
	DBAddress = "localhost"
	DBPort    = 3322

	DBName            = "defaultdb"
	State_Path_Hidden = "./.immudb_state"

	// Constants for the accounts database
	AccountsDBName = "accountsdb"

	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)

var (
	DBUsername = "immudb"
	DBPassword = "immudb"
)

// Explorer API authentication values used at runtime.
// These are set at runtime (e.g. via CLI handlers) and intentionally have no hard-coded defaults in the codebase.
var (
	EXPLORER_API_KEY string
	JWT_SECRET       string
)

// ImmuClient provides a simplified interface for ImmuDB operations
type ImmuClient struct {
	Client      client.ImmuClient
	Ctx         context.Context
	Cancel      context.CancelFunc
	BaseCtx     context.Context
	RetryLimit  int
	IsConnected bool
	Logger      *logging.AsyncLogger
	Database    string
}

// BlockHasher for generating block hashes
type BlockHasher struct{}

// ImmuTransaction represents a transaction in ImmuDB
type ImmuTransaction struct {
	Client *ImmuClient
	Ops    []*schema.Op
}
