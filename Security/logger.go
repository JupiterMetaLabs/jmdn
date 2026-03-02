package Security

import (
	log "jmdn/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Security, "")
	if err != nil {
		return nil
	}
	// Return the NamedLogger which is *ion.Ion
	return logInstance.NamedLogger
}
