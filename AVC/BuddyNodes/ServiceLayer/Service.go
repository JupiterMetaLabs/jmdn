package ServiceLayer

import (
	"context"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"sync"
)

var (
	Once          sync.Once
	ServiceController *Types.Controller
)

func InitService(controller *Types.Controller) {
	DataLayer.GetCRDTLayer()
}

func GetServiceController() *Types.Controller {
	Once.Do(func() {
		ServiceController = DataLayer.GetCRDTLayer()
	})
	return ServiceController
}

func Controller(controller *Types.Controller, OP *Types.OP) interface{} {
	// This is the abstractions layer for the CRDT layer
	switch OP.OpType {
	case Types.ADD:
		return DataLayer.Add(controller, OP.NodeID, OP.KeyValue.Key, OP.KeyValue.Value)
	case Types.REMOVE:
		return DataLayer.Remove(controller, OP.NodeID, OP.KeyValue.Key, OP.KeyValue.Value)
	case Types.SYNC:
		// For sync, we need to get the remote controller from the network
		// This is a placeholder - you'll need to implement node discovery - TODO
		remoteController := DataLayer.GetCRDTLayer() // Get remote node's controller
		return DataLayer.SyncWithNode(context.Background(), controller, remoteController, "local", OP.NodeID.String())
	}
	return nil
}
