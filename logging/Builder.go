package logging

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// LoggerBuilder implements the builder pattern for creating loggers
type LoggerBuilder struct {
	config *Logging
}

// Global logger instance (singleton)
var (
	globalLogger *AsyncLogger
	loggerOnce   sync.Once
	loggerMutex  sync.RWMutex
)

// NewLoggerBuilder creates a new LoggerBuilder instance
func NewLoggerBuilder() *LoggerBuilder {
	return &LoggerBuilder{
		config: &Logging{
			Metadata: LoggingMetadata{
				DIR:       "logs",
				BatchSize: 100,
				BatchWait: 2 * time.Second,
				Timeout:   6 * time.Second,
				KeepLogs:  true,
			},
		},
	}
}

// SetFileName sets the log file name
func (lb *LoggerBuilder) SetFileName(fileName string) *LoggerBuilder {
	lb.config.FileName = fileName
	return lb
}
func (lb *LoggerBuilder) GetFileName() string {
	return lb.config.FileName
}

// SetTopic sets the log topic
func (lb *LoggerBuilder) SetTopic(topic string) *LoggerBuilder {
	lb.config.Topic = topic
	return lb
}

// GetTopic returns the current topic
func (lb *LoggerBuilder) GetTopic() string {
	return lb.config.Topic
}

// SetURL sets the Loki URL
func (lb *LoggerBuilder) SetURL(url string) *LoggerBuilder {
	lb.config.URL = url
	return lb
}

func (lb *LoggerBuilder) GetURL() string {
	return lb.config.URL
}

// SetDirectory sets the log directory
func (lb *LoggerBuilder) SetDirectory(dir string) *LoggerBuilder {
	lb.config.Metadata.DIR = dir
	return lb
}

func (lb *LoggerBuilder) GetDirectory() string {
	return lb.config.Metadata.DIR
}

// SetBatchSize sets the batch size for Loki
func (lb *LoggerBuilder) SetBatchSize(size int) *LoggerBuilder {
	lb.config.Metadata.BatchSize = size
	return lb
}

func (lb *LoggerBuilder) GetBatchSize() int {
	return lb.config.Metadata.BatchSize
}

// SetBatchWait sets the batch wait time for Loki
func (lb *LoggerBuilder) SetBatchWait(wait time.Duration) *LoggerBuilder {
	lb.config.Metadata.BatchWait = wait
	return lb
}

func (lb *LoggerBuilder) GetBatchWait() time.Duration {
	return lb.config.Metadata.BatchWait
}

// SetTimeout sets the timeout for Loki
func (lb *LoggerBuilder) SetTimeout(timeout time.Duration) *LoggerBuilder {
	lb.config.Metadata.Timeout = timeout
	return lb
}

func (lb *LoggerBuilder) GetTimeout() time.Duration {
	return lb.config.Metadata.Timeout
}

// SetKeepLogs sets whether to keep logs
func (lb *LoggerBuilder) SetKeepLogs(keep bool) *LoggerBuilder {
	lb.config.Metadata.KeepLogs = keep
	return lb
}

func (lb *LoggerBuilder) GetKeepLogs() bool {
	return lb.config.Metadata.KeepLogs
}

// Build creates and returns the AsyncLogger
func (lb *LoggerBuilder) Build() (*AsyncLogger, error) {
	return NewAsyncLogger(lb.config)
}

// BuildAndSetGlobal creates the logger and sets it as the global logger
func (lb *LoggerBuilder) BuildAndSetGlobal() (*AsyncLogger, error) {
	logger, err := lb.Build()
	if err != nil {
		return nil, err
	}
	SetGlobalLogger(logger)
	return logger, nil
}

// SetGlobalLogger sets the global logger instance
func SetGlobalLogger(logger *AsyncLogger) {
	loggerMutex.Lock()
	defer loggerMutex.Unlock()
	globalLogger = logger
}

// GetGlobalLogger returns the global logger instance
func GetGlobalLogger() *AsyncLogger {
	loggerMutex.RLock()
	defer loggerMutex.RUnlock()
	return globalLogger
}

// GetOrCreateGlobalLogger returns the global logger, creating it if it doesn't exist
func GetOrCreateGlobalLogger() *AsyncLogger {
	if globalLogger == nil {
		loggerOnce.Do(func() {
			// Create a default logger if none exists
			builder := NewLoggerBuilder().
				SetFileName("default.log").
				SetTopic("default")

			if logger, err := builder.Build(); err == nil {
				globalLogger = logger
			}
		})
	}
	return globalLogger
}

// Quick builder methods for common scenarios
func NewDefaultLogger() *LoggerBuilder {
	return NewLoggerBuilder().
		SetFileName("gossipnode.log").
		SetTopic("gossipnode").
		SetURL("") // No Loki by default
}

func NewComponentLogger(component string) *LoggerBuilder {
	return NewLoggerBuilder().
		SetFileName(fmt.Sprintf("%s.log", component)).
		SetTopic(component).
		SetURL("") // No Loki by default
}

func NewFileOnlyLogger(fileName string) *LoggerBuilder {
	return NewLoggerBuilder().
		SetFileName(fileName).
		SetURL("") // No Loki, file only
}

func NewLokiOnlyLogger(topic string) *LoggerBuilder {
	return NewLoggerBuilder().
		SetFileName(""). // No file, Loki only
		SetTopic(topic).
		SetURL(GetLokiURL())
}

// Convenience methods for quick logging
func LogInfo(message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		logger.Logger.Info(message, fields...)
	}
}

func LogError(message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		logger.Logger.Error(message, fields...)
	}
}

func LogDebug(message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		logger.Logger.Debug(message, fields...)
	}
}

func LogWarn(message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		logger.Logger.Warn(message, fields...)
	}
}

// Component-specific logging
func LogWithComponent(component, level, message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		componentLogger := logger.GetSubLogger(component)
		switch level {
		case "info":
			componentLogger.Info(message, fields...)
		case "error":
			componentLogger.Error(message, fields...)
		case "debug":
			componentLogger.Debug(message, fields...)
		case "warn":
			componentLogger.Warn(message, fields...)
		default:
			componentLogger.Info(message, fields...)
		}
	}
}

// Topic-specific logging
func LogWithTopic(topic, level, message string, fields ...zap.Field) {
	if logger := GetGlobalLogger(); logger != nil {
		topicLogger := logger.WithTopic(topic)
		switch level {
		case "info":
			topicLogger.Info(message, fields...)
		case "error":
			topicLogger.Error(message, fields...)
		case "debug":
			topicLogger.Debug(message, fields...)
		case "warn":
			topicLogger.Warn(message, fields...)
		default:
			topicLogger.Info(message, fields...)
		}
	}
}

// JSON logging helper
func LogJSON(level, message string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		LogError("Failed to marshal JSON data", zap.Error(err))
		return
	}

	fields := []zap.Field{zap.String("json_data", string(jsonData))}

	switch level {
	case "info":
		LogInfo(message, fields...)
	case "error":
		LogError(message, fields...)
	case "debug":
		LogDebug(message, fields...)
	case "warn":
		LogWarn(message, fields...)
	default:
		LogInfo(message, fields...)
	}
}

// Close global logger
func CloseGlobalLogger() {
	if logger := GetGlobalLogger(); logger != nil {
		logger.Close()
		SetGlobalLogger(nil)
	}
}
