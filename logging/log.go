package logging

import (
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/rs/zerolog"
)

var (
    Log    zerolog.Logger
    logMu  sync.Mutex
    writer io.Writer
)

// InitLogger sets up logging to both console and file
func InitLogger(logDir, logFileName string, consoleOutput bool) error {
    logMu.Lock()
    defer logMu.Unlock()

    // Create logs directory if it doesn't exist
    if err := os.MkdirAll(logDir, 0755); err != nil {
        return fmt.Errorf("failed to create log directory: %w", err)
    }

    // Create or open log file
    logFilePath := filepath.Join(logDir, logFileName)
    logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
    if err != nil {
        return fmt.Errorf("failed to open log file: %w", err)
    }

    // Set up the writer (file and optionally console)
    if consoleOutput {
        writer = zerolog.MultiLevelWriter(logFile, zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
    } else {
        writer = logFile
    }

    // Configure the logger
    zerolog.TimeFieldFormat = time.RFC3339
    Log = zerolog.New(writer).With().Timestamp().Caller().Logger()

    Log.Info().Msg("Logger initialized")
    return nil
}

// Close closes the log file
func Close() {
    logMu.Lock()
    defer logMu.Unlock()

    if closer, ok := writer.(io.Closer); ok {
        closer.Close()
    }
}

// GetSubLogger creates a named sub-logger
func GetSubLogger(component string) zerolog.Logger {
    return Log.With().Str("component", component).Logger()
}