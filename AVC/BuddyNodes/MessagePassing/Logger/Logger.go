package Logger

import (
	"fmt"
	"gossipnode/logging"
	"time"

	"go.uber.org/zap"
)

// < -- Constants for buddy nodes logging -- >
const (
	LOG_FILE        = "buddy_nodes.log"
	Consensus_TOPIC = "buddy_nodes/pubsub"
	Messages_TOPIC  = "buddy_nodes/messages"
	DIR             = "logs"
	BatchSize       = 100
	BatchWait       = 2 * time.Second
	Timeout         = 6 * time.Second
	KeepLogs        = true
)

// < -- Singleton pattern for the logging -- >
var (
	ConsensusLogger *logging.AsyncLogger
	MessagesLogger  *logging.AsyncLogger
)

// Set Global variable for the logging
func SetGlobalVariables(Loggingstruct *logging.AsyncLogger, TOPIC string) {
	switch TOPIC {
	case Consensus_TOPIC:
		ConsensusLogger = Loggingstruct
	case Messages_TOPIC:
		MessagesLogger = Loggingstruct
	default:
		panic(fmt.Sprintf("invalid topic: %s", TOPIC))
	}
}

type LoggerBuilder struct {
	logger *logging.LoggerBuilder
}

func NewLoggerBuilder() *LoggerBuilder {
	return &LoggerBuilder{
		logger: logging.NewLoggerBuilder(),
	}
}

func (lb *LoggerBuilder) SetFileName(fileName string) *LoggerBuilder {
	lb.logger.SetFileName(fileName)
	return lb
}

func (lb *LoggerBuilder) SetTopic(topic string) *LoggerBuilder {
	lb.logger.SetTopic(topic)
	return lb
}

func (lb *LoggerBuilder) SetDirectory(directory string) *LoggerBuilder {
	lb.logger.SetDirectory(directory)
	return lb
}

func (lb *LoggerBuilder) SetBatchSize(batchSize int) *LoggerBuilder {
	lb.logger.SetBatchSize(batchSize)
	return lb
}

func (lb *LoggerBuilder) SetBatchWait(batchWait time.Duration) *LoggerBuilder {
	lb.logger.SetBatchWait(batchWait)
	return lb
}

func (lb *LoggerBuilder) SetTimeout(timeout time.Duration) *LoggerBuilder {
	lb.logger.SetTimeout(timeout)
	return lb
}

func (lb *LoggerBuilder) SetKeepLogs(keepLogs bool) *LoggerBuilder {
	lb.logger.SetKeepLogs(keepLogs)
	return lb
}

func (lb *LoggerBuilder) SetURL(url string, isActive bool) *LoggerBuilder {
	if isActive {
		lb.logger.SetURL(url)
	} else {
		lb.logger.SetURL("")
	}
	return lb
}

func (lb *LoggerBuilder) Build() (*logging.AsyncLogger, error) {
	Logger, err := lb.logger.Build()
	if err != nil {
		return nil, err
	}
	SetGlobalVariables(Logger, lb.logger.GetTopic())
	return Logger, nil
}

// < -- Convenience Functions for Logging -- >

// LogConsensusInfo logs consensus info
func LogConsensusInfo(message string, fields ...zap.Field) {
	if ConsensusLogger != nil {
		allFields := append(fields, zap.String("topic", Consensus_TOPIC))
		ConsensusLogger.Logger.Info(message, allFields...)
	}
}

// LogConsensusError logs consensus error
func LogConsensusError(message string, err error, fields ...zap.Field) {
	if ConsensusLogger != nil {
		allFields := append(fields, zap.Error(err), zap.String("topic", Consensus_TOPIC))
		ConsensusLogger.Logger.Error(message, allFields...)
	}
}

// LogConsensusDebug logs consensus debug
func LogConsensusDebug(message string, fields ...zap.Field) {
	if ConsensusLogger != nil {
		allFields := append(fields, zap.String("topic", Consensus_TOPIC))
		ConsensusLogger.Logger.Debug(message, allFields...)
	}
}

// LogMessagesInfo logs messages info
func LogMessagesInfo(message string, fields ...zap.Field) {
	if MessagesLogger != nil {
		allFields := append(fields, zap.String("topic", Messages_TOPIC))
		MessagesLogger.Logger.Info(message, allFields...)
	}
}

// LogMessagesError logs messages error
func LogMessagesError(message string, err error, fields ...zap.Field) {
	if MessagesLogger != nil {
		allFields := append(fields, zap.Error(err), zap.String("topic", Messages_TOPIC))
		MessagesLogger.Logger.Error(message, allFields...)
	}
}

// LogMessagesDebug logs messages debug
func LogMessagesDebug(message string, fields ...zap.Field) {
	if MessagesLogger != nil {
		allFields := append(fields, zap.String("topic", Messages_TOPIC))
		MessagesLogger.Logger.Debug(message, allFields...)
	}
}

// CloseAllLoggers closes all logger instances
func CloseAllLoggers() {
	if ConsensusLogger != nil {
		ConsensusLogger.Close()
	}
	if MessagesLogger != nil {
		MessagesLogger.Close()
	}
}
