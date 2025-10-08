package Service

import (
	"sync"
	"time"

	"gossipnode/logging"
)

// intilize the async logger from gossipnode/logging package using sync.Once reuse the same logger for the whole application
var Once sync.Once
var Logger *logging.AsyncLogger

const(
	LOG_FILE = "gETH.log"
	TOPIC = "gETH"
	URL = "http://localhost:3100/loki/api/v1/push"
	DIR = "logs"
	BatchSize = 128 * 1024
)

func InitLogger() error {
	var err error
	Logger, err = logging.NewAsyncLogger(&logging.Logging{
		FileName: LOG_FILE,
		Topic:    TOPIC,
		URL:      URL,
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
