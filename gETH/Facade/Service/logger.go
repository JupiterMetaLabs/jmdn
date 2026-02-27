package Service

import (
	"context"
	"fmt"
	"sync"
	"time"

	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_FILE = ""
	TOPIC    = "gETH_Facade"
)

// ServiceLogger wraps the ion logger with OpenTelemetry support
type ServiceLogger struct {
	ionLogger *ion.Ion
	once      sync.Once
}

// Zero allocation logger - its already allocated in the asynclogger
func logger() *ion.Ion {
	// Panic safety: recover if settings not loaded yet (though lazy init should prevent this)
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Warning: Service logger init failed (settings not loaded?): %v\n", r)
		}
	}()

	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Facade, "")
	if err != nil {
		return nil
	}
	// Return the NamedLogger which is *ion.Ion
	return logInstance.NamedLogger
}

// Global Logger instance - Lazy initialized
var Logger = &ServiceLogger{}

// LogData logs data with OpenTelemetry spans and traces
// ctx: context for the operation
// message: log message
// functionName: name of the function being logged
// status: 1 for success, -1 for error, 0 for info
func (l *ServiceLogger) LogData(ctx context.Context, message string, functionName string, status int) error {
	// Lazy initialization on first use
	l.once.Do(func() {
		l.ionLogger = logger()
	})

	if l.ionLogger == nil {
		// Fail silently or print to stdout if logger setup failed
		// fmt.Printf("[FluxLogger Fallback] %s: %s\n", functionName, message)
		return fmt.Errorf("logger not initialized")
	}

	// Create span for the operation
	spanCtx, span := l.ionLogger.Tracer("gETH_Facade").Start(ctx, fmt.Sprintf("gETH_Facade.%s", functionName))
	defer span.End()

	startTime := time.Now().UTC()

	// Set span attributes
	span.SetAttributes(
		attribute.String("function", functionName),
		attribute.String("message", message),
		attribute.Int("status", status),
	)

	// Determine log level based on status
	switch status {
	case -1:
		// Error case
		span.SetAttributes(attribute.String("log_level", "error"))
		span.RecordError(fmt.Errorf("%s", message))
		l.ionLogger.Error(spanCtx, message,
			fmt.Errorf("%s", message),
			ion.String("function", functionName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
		)
	case 1:
		// Success case
		span.SetAttributes(attribute.String("log_level", "info"), attribute.String("status", "success"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		l.ionLogger.Info(spanCtx, message,
			ion.String("function", functionName),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
		)
	default:
		// Info case
		span.SetAttributes(attribute.String("log_level", "info"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		l.ionLogger.Info(spanCtx, message,
			ion.String("function", functionName),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
		)
	}

	return nil
}
