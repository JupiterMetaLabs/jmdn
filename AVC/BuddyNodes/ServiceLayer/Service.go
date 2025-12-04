package ServiceLayer

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	AppContext "gossipnode/config/Context"
	"sync"
)

var (
	Once               sync.Once
	ServiceController  *Types.Controller
	ServiceLayerAppContext = "avc.buddynodes.servicelayer"
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
		// Use the node discovery service to find and sync with the remote peer
		remoteController := DataLayer.GetCRDTLayer() // Get remote node's controller
		ctx, _ := AppContext.GetAppContext(ServiceLayerAppContext).NewChildContext()
		return DataLayer.SyncWithNode(ctx, controller, remoteController, "local", OP.NodeID.String())
	}
	return fmt.Errorf("invalid operation type: %d", OP.OpType)
}
