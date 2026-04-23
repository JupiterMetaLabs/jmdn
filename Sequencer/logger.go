package Sequencer

import (
	log "gossipnode/logging"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *log.Logging {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(log.Sequencer, "")
	if err != nil {
		return nil
	}
	return logger
}
