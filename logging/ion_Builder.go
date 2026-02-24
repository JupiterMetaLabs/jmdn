package logging

import (
	"context"
	"fmt"
	"sync"

	"gossipnode/logging/otelsetup"

	"github.com/JupiterMetaLabs/ion"
)

var ionLoggeing_DIR = "logs"
var ionLogging_FileName = ""
var Once sync.Once
var asyncLogger *AsyncLogger

func NewAsyncLogger() *AsyncLogger {
	Once.Do(func() {
		asyncLogger = &AsyncLogger{}
		asyncLogger.Logging = make(map[string]Logging)
		var err error
		asyncLogger.GlobalLogger, err = asyncLogger.setGlobal()
		if err != nil {
			panic(fmt.Sprintf("FATAL: failed to set global logger: %v", err))
		}
	})
	return asyncLogger
}

func (al *AsyncLogger) Get() *AsyncLogger {
	if al.GlobalLogger == nil {
		return nil
	}
	return al
}

func (al *AsyncLogger) setGlobal() (*ion.Ion, error) {
	// If the global logger is already initialized, return the existing logger
	if al.GlobalLogger != nil {
		return al.GlobalLogger, nil
	}
	// Setup the global logger
	ionInstance, _, err := otelsetup.Setup(ionLoggeing_DIR, ionLogging_FileName)
	if err != nil {
		return nil, err
	}

	return ionInstance, nil
}

func (al *AsyncLogger) NamedLogger(topic string, fileName string) (*Logging, error) {
	// Defensive nil-check
	if al.Logging == nil {
		al.Logging = make(map[string]Logging)
	}
	// If the named logger is already created, return the existing logger
	if al.Logging[topic].NamedLogger != nil {
		namedLogger, ok := al.Logging[topic]
		if !ok {
			return nil, fmt.Errorf("Something went wrong inside duplicate check '%s' not found - this should not happen", topic)
		}
		return &namedLogger, nil
	}
	// Get the named logger instance
	// Named() returns ion.Logger (interface), but we need *ion.Ion for Tracer() method
	// Since we need *ion.Ion for Tracer() calls, we'll use the GlobalLogger directly
	// and add the topic as a field in log calls. The Named() logger can be used for
	// actual logging, but for Tracer() we need the *ion.Ion instance.
	//
	// For now, we'll store the GlobalLogger (*ion.Ion) directly so Tracer() works.
	// The topic will be added as a field in log calls via ion.String("topic", topic)
	namedLoggerPtr := al.GlobalLogger

	// Store the logger in the map
	al.Logging[topic] = Logging{
		Topic:       topic,
		FileName:    fileName,
		NamedLogger: namedLoggerPtr,
	}
	namedLogger, ok := al.Logging[topic]
	if !ok {
		return nil, fmt.Errorf("Something went wrong '%s' not found - this should not happen", topic)
	}
	return &namedLogger, nil
}

func (al *AsyncLogger) GetNamedLogger(topic string) (*Logging, error) {
	// Defensive nil-check
	if al.Logging == nil {
		return nil, fmt.Errorf("Logging map is not initialized")
	}
	// Get the named logger instance
	namedLogger, ok := al.Logging[topic]
	if !ok {
		return nil, fmt.Errorf("Named logger for topic '%s' not found", topic)
	}
	return &namedLogger, nil
}

func (al *AsyncLogger) Sync() error {
	return al.GlobalLogger.Sync()
}

func (al *AsyncLogger) Shutdown() error {
	// GlobalLogger is already *ion.Ion, no type assertion needed
	if al.GlobalLogger == nil {
		return fmt.Errorf("GlobalLogger is not initialized")
	}
	return otelsetup.Shutdown(context.Background(), al.GlobalLogger)
}

func (al *AsyncLogger) Close(topic string) error {
	namedLogger, err := al.GetNamedLogger(topic)
	if err != nil {
		return err
	}
	return (*namedLogger).Close()
}
