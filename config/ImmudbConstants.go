package config

import (
	"context"
	"fmt"
	"gossipnode/logging"
	"log"
	"os"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
)

const (
	// Database connection settings
	DBAddress         = "localhost"
	DBPort            = 3322
	DBUsername        = "immudb"
	DBPassword        = "immudb"
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

func ProcessLogs(al *AsyncLogger) {
	defer al.Wg.Done()

	for msg := range al.LogChan {
		al.Logger.Println(msg)
	}
}

// LogMessage sends a log message to the channel
func LogMessage(al *AsyncLogger, level, format string, args ...interface{}) {
	msg := fmt.Sprintf(level+": "+format, args...)

	// Non-blocking send to channel with timeout
	select {
	case al.LogChan <- msg:
		// Message sent successfully
	case <-time.After(time.Millisecond * 10):
		// Channel is full, drop the message
	}
}

// Info logs an info message
func Info(al *AsyncLogger, format string, args ...interface{}) {
	LogMessage(al, "INFO", format, args...)
}

// Warning logs a warning message
func Warning(al *AsyncLogger, format string, args ...interface{}) {
	LogMessage(al, "WARNING", format, args...)
}

// Error logs an error message
func Error(al *AsyncLogger, format string, args ...interface{}) {
	LogMessage(al, "ERROR", format, args...)
}

// Close closes the logger
func Close(al *AsyncLogger) error {
	// Close channel and wait for worker to finish
	close(al.LogChan)
	al.Wg.Wait()

	// Close file
	return al.File.Close()
}
