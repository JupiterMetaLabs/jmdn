package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/JupiterMetaLabs/ion"
)

func NewLoggingBuilder() *Logging {
	return &Logging{}
}

func (lb *Logging) SetFileName(fileName string) *Logging {
	lb.FileName = fileName
	return lb
}

func (lb *Logging) SetTopic(topic string) *Logging {
	lb.Topic = topic
	return lb
}

func (lb *Logging) NewNamedLogger(globalLogger *ion.Ion) *Logging {
	// Named() returns ion.Logger (interface), but we need *ion.Ion
	// Type assert ion.Logger interface to *ion.Ion pointer
	ionLogger := globalLogger.Named(lb.Topic)
	namedLoggerPtr, ok := ionLogger.(*ion.Ion)
	if !ok {
		// If type assertion fails, we can't store it
		// This should not happen if globalLogger is *ion.Ion
		return lb
	}
	lb.NamedLogger = namedLoggerPtr
	return lb
}

func (lb *Logging) GetNamedLogger() *ion.Ion {
	return lb.NamedLogger
}

func (lb *Logging) GetLoggingMetadata() *LoggingMetadata {
	return lb.LoggingMetadata
}

func (lb *Logging) SetLoggingMetadata(loggingMetadata *LoggingMetadata) *Logging {
	lb.LoggingMetadata = loggingMetadata
	return lb
}

func (lb *Logging) Close() error {
	if lb.NamedLogger == nil {
		return fmt.Errorf("NamedLogger is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return lb.NamedLogger.Shutdown(ctx)
}

func (lb *Logging) Sync() error {
	if lb.NamedLogger == nil {
		return fmt.Errorf("NamedLogger is not initialized")
	}
	return lb.NamedLogger.Sync()
}
