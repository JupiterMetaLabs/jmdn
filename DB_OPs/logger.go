package DB_OPs

import (
	log "jmdn/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger(NamedLogger string) *ion.Ion {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(NamedLogger, "")
	if err != nil {
		return nil
	}
	return logger.GetNamedLogger()
}
