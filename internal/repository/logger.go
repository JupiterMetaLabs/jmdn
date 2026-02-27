package repository

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

const (
	// tracerName is the OpenTelemetry tracer name used for all coordinator-level spans.
	tracerName = "DBCoordinator"
)

// repoLogger returns the *ion.Ion instance for the DBCoordinator named logger.
// Zero-allocation on the hot path because the logger is already allocated in the asynclogger.
func repoLogger() *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(log.DBCoordinator, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}
