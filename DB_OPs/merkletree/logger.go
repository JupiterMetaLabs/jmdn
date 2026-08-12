package merkletree

import (
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

func logger(NamedLogger string) *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(NamedLogger, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}
