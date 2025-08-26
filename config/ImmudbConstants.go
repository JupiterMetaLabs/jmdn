package config

import (
	"context"
	"gossipnode/logging"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
)

const (
	// Database connection settings
	DBAddress       = "0.0.0.0"
	DBPort          = 3322
	DBUsername      = "immudb"
	DBPassword      = "immudb"
	DBName          = "defaultdb"
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
	Database  	string
	Logger      *logging.AsyncLogger
}

// BlockHasher for generating block hashes
type BlockHasher struct{}

// ImmuTransaction represents a transaction in ImmuDB
type ImmuTransaction struct {
	Client *ImmuClient
	Ops    []*schema.Op
}

