package adapters

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// logger mirrors Security/logger.go's pattern: a zero-allocation lookup of
// the already-initialized async logger, named for this package so its output
// is independently greppable/filterable in production (log:AvcAdapter).
func logger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.AvcAdapter, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}
