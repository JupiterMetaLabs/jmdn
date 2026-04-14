package evm

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// logger returns the ion structured logger for the evm package.
// Zero allocation — the underlying logger is already initialised by the async logger singleton.
func evmLogger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.SmartContract, "")
	if err != nil || logInstance == nil {
		return nil
	}
	return logInstance.NamedLogger
}
