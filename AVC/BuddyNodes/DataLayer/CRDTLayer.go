package DataLayer

import (
	"context"
	"fmt"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/crdt"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"
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

func Add(controller *Types.Controller, nodeID multiaddr.Multiaddr, key string, value string) error {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Use the CRDT Engine to add to LWW Set
	return controller.CRDTLayer.LWWAdd(nodeID.String(), key, value, nil)
}

func Remove(controller *Types.Controller, nodeID multiaddr.Multiaddr, key string, value string) error {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Use the CRDT Engine to remove from LWW Set
	return controller.CRDTLayer.LWWRemove(nodeID.String(), key, value, nil)
}

func GetSet(controller *Types.Controller, key string) ([]string, bool) {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Get all elements from the LWW Set
	return controller.CRDTLayer.GetSet(key)
}

func CounterInc(controller *Types.Controller, nodeID multiaddr.Multiaddr, key string, value uint64) error {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Increment a counter
	return controller.CRDTLayer.CounterInc(nodeID.String(), key, value, nil)
}

func GetCounter(controller *Types.Controller, key string) (uint64, bool) {
	_, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Get counter value
	return controller.CRDTLayer.GetCounter(key)
}

// SyncWithNode syncs CRDTs between two nodes by merging their states
func SyncWithNode(ctx context.Context, localController, remoteController *Types.Controller, localNodeID, remoteNodeID string) error {
	// Get all CRDTs from both nodes
	localCRDTs := localController.CRDTLayer.GetAllCRDTs()
	remoteCRDTs := remoteController.CRDTLayer.GetAllCRDTs()

	// Merge remote CRDTs into local node
	for key, remoteCRDT := range remoteCRDTs {
		if localCRDT, exists := localCRDTs[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := localCRDT.Merge(remoteCRDT)
			if err != nil {
				return fmt.Errorf("failed to merge CRDT %s: %v", key, err)
			}
			// Apply the merged CRDT back to local node
			applyMergedCRDT(localController.CRDTLayer, key, merged)
		} else {
			// Only remote node has this CRDT, copy it to local node
			applyMergedCRDT(localController.CRDTLayer, key, remoteCRDT)
		}
	}

	// Merge local CRDTs into remote node
	for key, localCRDT := range localCRDTs {
		if remoteCRDT, exists := remoteCRDTs[key]; exists {
			// Both nodes have this CRDT, merge them
			merged, err := remoteCRDT.Merge(localCRDT)
			if err != nil {
				return fmt.Errorf("failed to merge CRDT %s: %v", key, err)
			}
			applyMergedCRDT(remoteController.CRDTLayer, key, merged)
		} else {
			// Only local node has this CRDT, copy it to remote node
			applyMergedCRDT(remoteController.CRDTLayer, key, localCRDT)
		}
	}

	return nil
}

// SyncAllNodes syncs CRDTs between all nodes in a network
func SyncAllNodes(ctx context.Context, nodes map[string]*Types.Controller) error {
	nodeIDs := make([]string, 0, len(nodes))
	for nodeID := range nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}

	// Sync each pair of nodes
	for i := 0; i < len(nodeIDs); i++ {
		for j := i + 1; j < len(nodeIDs); j++ {
			node1ID := nodeIDs[i]
			node2ID := nodeIDs[j]
			fmt.Println("Syncing", node1ID, "with", node2ID)
			if err := SyncWithNode(ctx, nodes[node1ID], nodes[node2ID], node1ID, node2ID); err != nil {
				return fmt.Errorf("failed to sync %s with %s: %v", node1ID, node2ID, err)
			}
		}
	}

	return nil
}

// GetCRDTState returns the current state of all CRDTs for export/sync
func GetCRDTState(ctx context.Context, controller *Types.Controller) map[string]crdt.CRDT {
	return controller.CRDTLayer.GetAllCRDTs()
}

// applyMergedCRDT applies a merged CRDT to a node's engine
// This uses the Engine's public method to apply the merged state
func applyMergedCRDT(engine *crdt.Engine, key string, crdt crdt.CRDT) {
	engine.ApplyMergedCRDT(key, crdt)
}
