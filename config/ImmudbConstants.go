package config

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/ion"
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

// ImmuClient is a legacy struct retained so existing call-sites in dualdb,
// immuclient.go, and account_immuclient.go compile without modification.
// The real storage backend is store.ThebeHandle (injected via config.HandleFactory).
// Fields that were removed from the remove/immudb branch are restored here
// to preserve compilation of the merged main branch code.
type ImmuClient struct {
	Ctx         context.Context
	Cancel      context.CancelFunc
	BaseCtx     context.Context
	RetryLimit  int
	IsConnected bool
	Logger      *ion.Ion
	Database    string
	// Client is the underlying ImmuDB gRPC client.
	// Legacy call-sites access it for KV operations (Get, Set, ExecAll, Scan, etc.).
	Client interface{}
}

// Close implements io.Closer so *ImmuClient can be stored in config.PooledConnection.Client.
// Delegates to Cancel() to release the underlying gRPC context.
func (ic *ImmuClient) Close() error {
	if ic != nil && ic.Cancel != nil {
		ic.Cancel()
	}
	return nil
}

// BlockHasher for generating block hashes
type BlockHasher struct{}

// ImmuTransaction is a legacy stub.
type ImmuTransaction struct {
	Client *ImmuClient
	Ops    []interface{}
}
