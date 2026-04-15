package messaging

import (
	"gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// contractLogger returns the named ion logger for the ContractPropagation subsystem.
func contractLogger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.ContractPropagation, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}

// broadcastLogger returns the named ion logger for the broadcast / block-propagation subsystem.
func broadcastLogger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.Broadcast, "")
	if err != nil {
		return nil
	}
	return logInstance.NamedLogger
}

