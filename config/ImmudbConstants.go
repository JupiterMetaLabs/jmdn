package config

import (
	"context"
	"time"

	"github.com/JupiterMetaLabs/ion"
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

// ImmuClient is a legacy stub retained so call-sites in dualdb and immuclient.go
// compile without modification. The real storage backend is now store.ThebeHandle
// (injected via config.HandleFactory). All immudb fields have been removed.
type ImmuClient struct {
	Ctx         context.Context
	Cancel      context.CancelFunc
	BaseCtx     context.Context
	RetryLimit  int
	IsConnected bool
	Logger      *ion.Ion
	Database    string
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

// ImmuTransaction is a legacy stub. Ops is kept as []interface{} to satisfy
// any callers that append to it without importing immudb schema types.
type ImmuTransaction struct {
	Client *ImmuClient
	Ops    []interface{}
}
