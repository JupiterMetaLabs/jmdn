package CLI

import (
	"gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
// logger returns the specific named logger for the CLI Server
func logger() *ion.Ion {
	// We use a specific topic for CLI Server
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger("CLI_Server", "")
	if err != nil {
		// Fallback or nil - though NewAsyncLogger().Get() should handle initialization
		return nil
	}
	// Return the NamedLogger which is *ion.Ion
	return logInstance.NamedLogger
}

// clientLogger returns the specific named logger for the CLI Client
func clientLogger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger("CLI_Client", "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}
