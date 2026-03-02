package explorer

import (
	log "jmdn/logging"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *log.Logging {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(log.Explorer, "")
	if err != nil {
		return nil
	}
	return logger
}
