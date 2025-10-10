package Types

import (
	"gossipnode/crdt"

	"github.com/multiformats/go-multiaddr"
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
	NodeID   multiaddr.Multiaddr
	OpType   int8
	KeyValue KeyValue
	VEC      crdt.VectorClock
}
