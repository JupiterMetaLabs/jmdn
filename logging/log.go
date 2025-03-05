package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gossipnode/metrics"

	"github.com/rs/zerolog"
)

var (
	Log    zerolog.Logger
	logMu  sync.Mutex
	writer io.Writer
)

// Create a hook for logging metrics
type metricsHook struct{}

func (h metricsHook) Run(e *zerolog.Event, level zerolog.Level, msg string) {
	metrics.LogEntries.WithLabelValues(level.String(), "unknown").Inc()
}

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
		consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
		consoleWriter.FormatLevel = func(i interface{}) string {
			return fmt.Sprintf("[%-5s]", i)
		}
		writer = zerolog.MultiLevelWriter(logFile, consoleWriter)
	} else {
		writer = logFile
	}

	// Configure the logger with metrics hook
	zerolog.TimeFieldFormat = time.RFC3339
	Log = zerolog.New(writer).With().Timestamp().Caller().Logger().Hook(metricsHook{})

	Log.Info().Msg("Logger initialized")

	// Log the file path to help debugging
	Log.Info().Str("log_file", logFilePath).Msg("Logging to file")
	return nil
}

// Make sure critical errors get logged
func LogFatal(err error, msg string) {
	if err != nil {
		Log.Fatal().Err(err).Msg(msg)
	}
}

// Close closes the log file
func Close() {
	logMu.Lock()
	defer logMu.Unlock()

	Log.Info().Msg("Closing logger")
	if closer, ok := writer.(io.Closer); ok {
		closer.Close()
	}
}

// GetSubLogger creates a named sub-logger
func GetSubLogger(component string) zerolog.Logger {
	return Log.With().Str("component", component).Logger()
}
