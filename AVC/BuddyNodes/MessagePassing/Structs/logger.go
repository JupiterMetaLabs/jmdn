package Structs

import (
	log "gossipnode/logging"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *log.Logging {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(log.MessagePassing_StructService, "")
	if err != nil {
		return nil
	}
	return logger
}
