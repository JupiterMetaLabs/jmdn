package DataLayer

import (
	"context"
	"sync"
	"time"

	crdt "github.com/JupiterMetaLabs/avc/crdt"

	"gossipnode/AVC/BuddyNodes/Types"

	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	Once      sync.Once
	CRDTLayer *Types.Controller
)

// NewCRDTLayer creates a new CRDT layer with the provided engine
func NewCRDTLayer(crdtEngine *crdt.Engine) *Types.Controller {
	if crdtEngine == nil {
		crdtEngine = crdt.NewEngineMemOnly(1024 * 1024 * 50) // 50MB memory limit
	}
	return &Types.Controller{CRDTLayer: crdtEngine}
}

// GetCRDTLayer returns the singleton CRDT layer instance
func GetCRDTLayer() *Types.Controller {
	Once.Do(func() {
		CRDTLayer = NewCRDTLayer(nil)
	})
	return CRDTLayer
}

func Add(controller *Types.Controller, nodeID peer.ID, key string, value string) error {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Use the CRDT Engine to add to LWW Set
	return controller.CRDTLayer.LWWAdd(nodeID.String(), key, value, nil)
}

func GetSet(controller *Types.Controller, key string) ([]string, bool) {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Get all elements from the LWW Set
	return controller.CRDTLayer.GetSet(key)
}
