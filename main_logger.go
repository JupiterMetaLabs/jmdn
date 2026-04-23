package main

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger - its already allocated in the asynclogger
func mainLogger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Main, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
