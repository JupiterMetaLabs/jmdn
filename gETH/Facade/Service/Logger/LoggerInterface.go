package Logger

import (
	"context"
	"fmt"
	"gossipnode/logging"
	AppContext "gossipnode/config/Context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const(
	LoggerAppContext = "gETH.facade.service.logger"
)

// intilize the async logger from gossipnode/logging package using sync.Once reuse the same logger for the whole application
var Once sync.Once
var Logger *logging.AsyncLogger

const (
	LOG_FILE  = "gETH.log"
	TOPIC     = "gETH"
	LOKI_URL  = "" // Disabled by default
	DIR       = "logs"
	BatchSize = 128 * 1024
)

func InitLogger() error {
	var err error
	Logger, err = logging.NewAsyncLogger(&logging.Logging{
		FileName: LOG_FILE,
		Topic:    TOPIC,
		URL:      "", // Disable Loki by default
		Metadata: logging.LoggingMetadata{
			DIR:       DIR,
			BatchSize: BatchSize,
			BatchWait: 2 * time.Second,
			Timeout:   10 * time.Second,
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func GetLogger() *logging.AsyncLogger {
	return Logger
}

func LogData(ctx context.Context, Message string, Function string, status int) error {
	// Create a new context with timeout for logging operation
	logCtx, cancel := AppContext.GetAppContext(LoggerAppContext).NewChildContextWithTimeout(3*time.Second)
	defer cancel()

	tempLogger := GetLogger()
	if tempLogger == nil || tempLogger.Logger == nil {
		return fmt.Errorf("logger is not initialized")
	}

	switch status {
	case 1:
		// Success
		tempLogger.Logger.Info(Message,
			zap.String(logging.Function, Function),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.Time(logging.Created_at, time.Now().UTC()),
		)
	case -1:
		// Error
		tempLogger.Logger.Error(Message,
			zap.String(logging.Function, Function),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.Time(logging.Created_at, time.Now().UTC()),
		)
	default:
		return fmt.Errorf("invalid status code: %d", status)
	}

	// Check if context was cancelled
	select {
	case <-logCtx.Done():
		return logCtx.Err()
	default:
		return nil
	}
}
