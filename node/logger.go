package node

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func logger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Node, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
