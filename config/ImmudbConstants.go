package config

import "time"

const (
	// Database connection settings
	DBAddress       = "0.0.0.0"
	DBPort          = 3322
	DBUsername      = "immudb"
	DBPassword      = "immudb"
	DBName          = "defaultdb"
	Table		   = "BlockchainDB"
	
	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)