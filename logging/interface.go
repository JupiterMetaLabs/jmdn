package logging

import "github.com/JupiterMetaLabs/ion"

type LoggingInterface interface {
	SetGlobal() error
	EnableOTEL(enabled bool, endpoint string) bool
	NamedLogger(topic string, fileName string) Logging
	GetNamedLogger() Logging
	Sync() error
	Shutdown() error // Named logger can't shutdown - reroute to close()
	Close() error
}

type AsyncLogger struct {
	GlobalLogger *ion.Ion
	Logging      map[string]Logging
}

type Logging struct {
	Topic           string
	FileName        string
	NamedLogger     *ion.Ion
	LoggingMetadata *LoggingMetadata
}

type LoggingMetadata struct {
	DIR      string
	KeepLogs bool
}
