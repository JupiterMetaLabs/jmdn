package Structs

import (
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/Types"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

var PubSub_BuddyNode *BuddyNode
var ForListner *BuddyNode

// ResponseHandler interface for handling ACK responses
type ResponseHandler interface {
	HandleResponse(peerID peer.ID, accepted bool)
}

type BuddyNode struct {
	CRDTLayer       *Types.Controller
	Host            host.Host
	Network         network.Network
	PeerID          peer.ID
	BuddyNodes      Buddies
	Mutex           sync.RWMutex
	MetaData        MetaData
	ResponseHandler ResponseHandler      // Interface for handling responses
	PubSub          *Struct.GossipPubSub // Will hold a reference to GossipPubSub instance
	StreamCache     *StreamCache         // LRU cache of reusable streams with TTL
}

type MetaData struct {
	Received  uint32
	Sent      uint32
	Total     uint32
	UpdatedAt time.Time
}

type Buddies struct {
	Buddies_Nodes []peer.ID
}


