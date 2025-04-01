package config

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
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
	Table		   = "BlockchainDB"

	// Constants for the accounts database
	AccountsDBName = "AccountsDB"
	
	// Operation settings
	DefaultScanLimit = 100
	RequestTimeout   = 10 * time.Second
)


// AsyncLogger provides asynchronous file logging
type AsyncLogger struct {
	Logger  *log.Logger
	LogChan chan string
	Wg      sync.WaitGroup
	File    *os.File
}

// ImmuClient provides a simplified interface for ImmuDB operations
type ImmuClient struct {
	Client      client.ImmuClient
	Ctx         context.Context
	Cancel      context.CancelFunc
	BaseCtx     context.Context
	Token       string
	RetryLimit  int
	IsConnected bool
	Logger      *AsyncLogger
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

// log sends a log message to the channel
func  Log(al *AsyncLogger ,level, format string, args ...interface{}) {
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
	Log(al, "INFO", format, args...)
}

// Warning logs a warning message
func Warning(al *AsyncLogger, format string, args ...interface{}) {
	Log(al, "WARNING", format, args...)
}

// Error logs an error message
func Error(al *AsyncLogger, format string, args ...interface{}) {
	Log(al, "ERROR", format, args...)
}

// Close closes the logger
func Close(al *AsyncLogger) error {
	// Close channel and wait for worker to finish
	close(al.LogChan)
	al.Wg.Wait()
	
	// Close file
	return al.File.Close()
}


