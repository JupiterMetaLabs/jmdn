package logging

import (
	"time"
)

type LoggingMetadata struct {
	DIR       string // Directory of the log file to be stored
	BatchSize int
	BatchWait time.Duration
	Timeout   time.Duration
	KeepLogs  bool
}

type Logging struct {
	FileName string
	Topic    string
	URL      string
	Metadata LoggingMetadata
}

// This is used to make logging parsing better and more reliable
const (
	// ConnectionPool for ImmuDB
	Created_at            = "created_at"
	Connection_created_at = "connection_created_at"
	Connection_last_used  = "connection_last_used"
	Connection_id         = "connection_id"
	Connection_database   = "connection_database"
	Log_file              = "log_file"
	Topic                 = "topic"
	Loki_url              = "loki_url"
	Function              = "function"
	Address               = "address"
	Port                  = "port"
	Username              = "username"
	ConnectionCount       = "connection_count"

	// AccountsImmuDBClient
	Account = "address"
	Count = "count"
	DID = "did"
	Header_Accounts = "header_accounts"
)