package bft

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *ion.Ion {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(log.BFT, "")
	if err != nil {
		return nil
	}
	return logger.NamedLogger
}
