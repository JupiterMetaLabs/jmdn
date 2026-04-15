package contractDB

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// logger returns the ion structured logger for the contractDB package.
// Zero allocation — the underlying logger is already initialised by the async logger singleton.
func logger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.ContractDB, "")
	if err != nil || logInstance == nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
