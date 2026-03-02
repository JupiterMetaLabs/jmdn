package Types

import (
	"jmdn/crdt"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// Operation flags
	ADD        = 1
	REMOVE     = -1
	SYNC       = 100
	COUNTERINC = 101
)

type Controller struct {
	CRDTLayer *crdt.Engine
}

type KeyValue struct {
	Key   string // String of the peer id of struct PubSubMessages.PeerID type peer.ID
	Value string // string of the json message of struct PubSubMessages.Vote.Vote. Vote is a int8
}

type OP struct {
	NodeID   peer.ID
	OpType   int8
	KeyValue KeyValue
	VEC      crdt.VectorClock
}
