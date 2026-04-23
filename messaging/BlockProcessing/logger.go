package BlockProcessing

import (
	"gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Zero allocation logger — already allocated in the asynclogger singleton.
func logger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.BlockProcessing, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}

