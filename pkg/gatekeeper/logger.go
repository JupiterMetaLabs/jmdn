package gatekeeper

import (
	log "jmdn/logging"

	"github.com/JupiterMetaLabs/ion"
)

// gatekeeperLogger returns a named logger instance from the async logger singleton.
// Returns nil if the logger system is not yet initialized (safe: callers must nil-check
// or use this only after logger initialization).
func gatekeeperLogger(namedLogger string) *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(namedLogger, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}

// logger is a convenience alias used by middleware files in this package.
var logger = gatekeeperLogger
