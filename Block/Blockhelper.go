package Block

import (
    "github.com/rs/zerolog"
    "sync"
)

var (
    txLogger    zerolog.Logger
    loggerMutex sync.RWMutex
)

// SetLogger sets the transaction logger for the Block package
func SetLogger(logger zerolog.Logger) {
    loggerMutex.Lock()
    defer loggerMutex.Unlock()
    txLogger = logger
}

// LogTransaction logs transaction data in structured format
func LogTransaction(hash, from, to, value, txType string) {
    loggerMutex.RLock()
    defer loggerMutex.RUnlock()
    
    // Ensure proper field names match what Promtail expects
    txLogger.Info().
        Str("transaction_hash", hash).
        Str("from", from).
        Str("to", to).
        Str("value", value).
        Str("type", txType).
        Msg("Transaction processed")
}