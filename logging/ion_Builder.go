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
	// Fast path: already exists
	al.mu.RLock()
	if al.Logging != nil {
		if entry, ok := al.Logging[topic]; ok && entry.NamedLogger != nil {
			al.mu.RUnlock()
			return &entry, nil
		}
	}
	al.mu.RUnlock()

	// Slow path: create
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.Logging == nil {
		al.Logging = make(map[string]Logging)
	}
	// Double-checked: another goroutine may have inserted while we waited
	if entry, ok := al.Logging[topic]; ok && entry.NamedLogger != nil {
		return &entry, nil
	}
	al.Logging[topic] = Logging{
		Topic:       topic,
		FileName:    fileName,
		NamedLogger: al.GlobalLogger,
	}
	entry := al.Logging[topic]
	return &entry, nil
}

func (al *AsyncLogger) GetNamedLogger(topic string) (*Logging, error) {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if al.Logging == nil {
		return nil, fmt.Errorf("Logging map is not initialized")
	}
	namedLogger, ok := al.Logging[topic]
	if !ok {
		return nil, fmt.Errorf("named logger for topic '%s' not found", topic)
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
