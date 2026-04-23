package DB_OPs

import (
	"sync"

	"gossipnode/config"
)

// ThebeShadowWriter is an optional best-effort fanout target for Thebe/Cassata
// ingestion. Primary immudb writes remain authoritative.
type ThebeShadowWriter interface {
	StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error
}

var (
	thebeShadowMu     sync.RWMutex
	thebeShadowWriter ThebeShadowWriter
)

func SetThebeShadowWriter(w ThebeShadowWriter) {
	thebeShadowMu.Lock()
	defer thebeShadowMu.Unlock()
	thebeShadowWriter = w
}

func getThebeShadowWriter() ThebeShadowWriter {
	thebeShadowMu.RLock()
	defer thebeShadowMu.RUnlock()
	return thebeShadowWriter
}
