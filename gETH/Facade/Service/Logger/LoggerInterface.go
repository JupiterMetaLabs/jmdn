package Logger

import (
	"context"
	"fmt"
	"gossipnode/logging"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
)

var Once sync.Once
var Logger *logging.AsyncLogger

const (
	LOG_FILE = "gETH.log"
	TOPIC    = "gETH"
	DIR      = "logs"
)

func InitLogger() error {
	var err error
	Once.Do(func() {
		Logger = logging.NewAsyncLogger()
		_, err = Logger.NamedLogger(TOPIC, LOG_FILE)
	})
	return err
}

func GetLogger() *logging.Logging {
	if Logger == nil {
		return nil
	}
	logger, err := Logger.GetNamedLogger(TOPIC)
	if err != nil {
		return nil
	}
	return logger
}

func LogData(ctx context.Context, Message string, Function string, status int) error {
	// Create a new context with timeout for logging operation
	logCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	logger := GetLogger()
	if logger == nil || logger.NamedLogger == nil {
		return fmt.Errorf("logger is not initialized")
	}

	spanCtx, span := logger.NamedLogger.Tracer("gETH").Start(logCtx, "gETH."+Function)
	defer span.End()

	switch status {
	case 1:
		// Success
		logger.NamedLogger.Info(spanCtx, Message,
			ion.String("function", Function),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		)
	case -1:
		// Error
		logger.NamedLogger.Error(spanCtx, Message,
			fmt.Errorf("gETH error logged"),
			ion.String("function", Function),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		)
	default:
		return fmt.Errorf("invalid status code: %d", status)
	}

	return nil
}
