package Cache

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func cacheLogger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
