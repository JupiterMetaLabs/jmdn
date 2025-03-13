package config

import "time"

const (
	// Database connection settings
	DBAddress       = "127.0.0.1"
	DBPort          = 3322
	DBUsername      = "immudb"
	DBPassword      = "immudb"
	DBName          = "defaultdb"
	
	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)