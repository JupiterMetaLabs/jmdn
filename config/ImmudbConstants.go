package config

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
)

// DBAddress and DBPort are vars so main.go can override them from config at
// startup (JMDN_DATABASE_ADDRESS / JMDN_DATABASE_PORT env vars or jmdn.yaml).
// All callers (fastsync, DB_OPs, ConnectionPool) read these vars — pointing
// at an external immudb container requires no changes in those packages.
var (
	DBAddress = "localhost"
	DBPort    = 3322
)

const (
	DBName            = "defaultdb"
	State_Path_Hidden = "./.immudb_state"

	// Constants for the accounts database
	AccountsDBName = "accountsdb"

	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)

// ImmuClient provides a simplified interface for ImmuDB operations
type ImmuClient struct {
	Client      client.ImmuClient
	Ctx         context.Context
	Cancel      context.CancelFunc
	BaseCtx     context.Context
	RetryLimit  int
	IsConnected bool
	Logger      *ion.Ion
	Database    string
}

// BlockHasher for generating block hashes
type BlockHasher struct{}

// ImmuTransaction represents a transaction in ImmuDB
type ImmuTransaction struct {
	Client *ImmuClient
	Ops    []*schema.Op
}
