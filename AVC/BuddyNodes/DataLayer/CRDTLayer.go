package DataLayer

import(
	// "context"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/crdt"

	// "github.com/multiformats/go-multiaddr"
)

func NewCRDTLayer(crdtEngine *crdt.Engine) *Types.Controller {
	if crdtEngine == nil {
		crdtEngine = crdt.NewEngineMemOnly(1024 * 1024 * 50) // 50MB memory limit
	}
	return &Types.Controller{CRDTLayer: crdtEngine}
}

// func Add(ctx context.Context, controller *Types.Controller, nodeID multiaddr.Multiaddr, key string, value string) error {
// 	op := &Types.OP{
// 		NodeID: nodeID,
// 		OpType: Types.ADD,
// 		KeyValue: Types.KeyValue{Key: key, Value: value},
// 		VEC: controller.CRDTLayer.GetTimestamp(),
// 	}
// 	return controller.CRDTLayer.LWWAdd(ctx, op)
// }