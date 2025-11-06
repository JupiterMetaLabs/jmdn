# Logging Module

## Overview

The Logging module provides structured logging capabilities for the JMZK network. It supports both file-based logging and centralized logging via Loki, with async logging for performance.

## Purpose

The Logging module enables:
- Structured logging with Zap logger
- File-based logging
- Centralized logging via Loki
- Async logging for performance
- Log level management
- Topic-based logging

## Key Components

### 1. Async Logger
**File:** `log.go`

Main logging implementation:
- `AsyncLogger`: Async logger with file and Loki support
- `NewAsyncLogger`: Create new async logger
- `ReturnDefaultLogger`: Create default logger
- `ReturnDefaultLoggerWithLoki`: Create logger with Loki support

### 2. Logger Builder
**File:** `Builder.go`

Builder pattern for logger configuration:
- `LoggerBuilder`: Builder for logger configuration
- `NewLoggerBuilder`: Create new logger builder
- `SetFileName`: Set log file name
- `SetTopic`: Set log topic
- `SetURL`: Set Loki URL
- `Build`: Build logger instance

### 3. Log Configuration
**File:** `LogConfig.go`

Logging configuration structures:
- `Logging`: Main logging configuration
- `LoggingMetadata`: Logging metadata configuration
- Log level constants
- Log format configuration

### 4. Loki Integration
**File:** `loki.go`

Loki client integration:
- `lokiWriteSyncer`: Loki write syncer
- `newLokiWriteSyncer`: Create new Loki write syncer
- Batch logging support
- Retry logic

## Key Functions

### Create Logger

```go
// Create default logger
logger, err := logging.ReturnDefaultLogger("app.log", "app")

// Create logger with Loki
logger, err := logging.ReturnDefaultLoggerWithLoki("app.log", "app", true)
```

### Using Logger Builder

```go
// Create logger with builder
logger, err := logging.NewLoggerBuilder().
    SetFileName("app.log").
    SetTopic("app").
    SetURL("http://localhost:3100/loki/api/v1/push").
    Build()
```

### Logging Messages

```go
// Log info message
logger.Logger.Info("Message", zap.String("key", "value"))

// Log error message
logger.Logger.Error("Error", zap.Error(err))

// Log debug message
logger.Logger.Debug("Debug", zap.String("key", "value"))

// Log warn message
logger.Logger.Warn("Warning", zap.String("key", "value"))
```

### Global Logger

```go
// Set global logger
logging.SetGlobalLogger(logger)

// Get global logger
logger := logging.GetGlobalLogger()

// Log with global logger
logging.LogInfo("Message", zap.String("key", "value"))
logging.LogError("Error", zap.Error(err))
```

## Usage

### Basic Logging

```go
import "gossipnode/logging"

// Create logger
logger, err := logging.ReturnDefaultLogger("app.log", "app")
if err != nil {
    log.Fatal(err)
}
defer logger.Close()

// Log messages
logger.Logger.Info("Application started")
logger.Logger.Error("Error occurred", zap.Error(err))
```

### Logging with Loki

```go
import "gossipnode/logging"

// Create logger with Loki
logger, err := logging.ReturnDefaultLoggerWithLoki("app.log", "app", true)
if err != nil {
    log.Fatal(err)
}
defer logger.Close()

// Log messages (automatically sent to Loki)
logger.Logger.Info("Application started")
```

### Using Logger Builder

```go
import "gossipnode/logging"

// Create logger with builder
logger, err := logging.NewLoggerBuilder().
    SetFileName("app.log").
    SetTopic("app").
    SetDirectory("logs").
    SetBatchSize(100).
    SetBatchWait(2 * time.Second).
    SetTimeout(6 * time.Second).
    SetURL("http://localhost:3100/loki/api/v1/push").
    Build()
if err != nil {
    log.Fatal(err)
}
defer logger.Close()

// Log messages
logger.Logger.Info("Application started")
```

### Global Logger

```go
import "gossipnode/logging"

// Set global logger
logger, _ := logging.ReturnDefaultLogger("app.log", "app")
logging.SetGlobalLogger(logger)

// Use global logger
logging.LogInfo("Message", zap.String("key", "value"))
logging.LogError("Error", zap.Error(err))
```

## Configuration

### Log File Configuration

- `FileName`: Log file name (e.g., "app.log")
- `Directory`: Log directory (default: "logs")
- `Topic`: Log topic for categorization

### Loki Configuration

- `URL`: Loki URL (default: "http://localhost:3100/loki/api/v1/push")
- `BatchSize`: Batch size for Loki (default: 100)
- `BatchWait`: Batch wait time (default: 2 seconds)
- `Timeout`: Timeout for Loki requests (default: 6 seconds)

### Environment Variables

- `LOKI_URL`: Override Loki URL from environment

## Integration Points

### All Modules
- All modules use logging for structured logging
- Consistent logging format across modules
- Centralized logging via Loki

### Config Module
- Uses logging for connection pool logging
- Logs database operations

### Node Module
- Uses logging for node operations
- Logs peer connections

### Block Module
- Uses logging for block operations
- Logs transaction processing

## Error Handling

The module includes comprehensive error handling:
- File creation errors
- Loki connection errors
- Batch processing errors
- Timeout errors

## Performance

- **Async Logging**: Non-blocking log writes
- **Batch Processing**: Efficient batch writes to Loki
- **Buffering**: Buffered writes for performance
- **Connection Pooling**: Efficient Loki connections

## Testing

Test files:
- `Test/log_test.go`: Logging tests
- Logger builder tests
- Loki integration tests

## Best Practices

1. **Use structured logging**: Include context with log messages
2. **Set appropriate log levels**: Use DEBUG, INFO, WARN, ERROR appropriately
3. **Close loggers**: Always close loggers on shutdown
4. **Use topics**: Categorize logs by topic
5. **Monitor Loki**: Monitor Loki for centralized logging

## Future Enhancements

- Enhanced log filtering
- Advanced log rotation
- Performance optimizations
- Additional log backends
- Log aggregation improvements

