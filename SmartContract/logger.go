package SmartContract

import (
	"gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// logger returns the named ion logger for the SmartContract subsystem.
func logger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.SmartContract, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}

