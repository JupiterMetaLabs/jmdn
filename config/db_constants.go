package config

import "time"

// DB-related constants for the ThebeDB-backed node.
const (
	// DBName / AccountsDBName are logical database labels used in structured
	// logging and the Explorer API. ThebeDB itself is a single embedded store.
	DBName         = "defaultdb"
	AccountsDBName = "accountsdb"

	// State_Path_Hidden is where locally generated TLS assets live.
	// The on-disk path is kept from the pre-migration layout so existing
	// deployments do not regenerate (and thereby rotate) their certificates.
	State_Path_Hidden = "./.immudb_state"

	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)
