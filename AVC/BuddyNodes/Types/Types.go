package Types

import (
	"gossipnode/crdt"

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
	Key   string
	Value string
}

type OP struct {
	NodeID   peer.ID
	OpType   int8
	KeyValue KeyValue
	VEC      crdt.VectorClock
}
