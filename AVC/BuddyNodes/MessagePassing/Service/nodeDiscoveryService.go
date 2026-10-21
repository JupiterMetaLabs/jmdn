package Service

import (
	"time"

	"gossipnode/AVC/BuddyNodes/Types"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
)

// NodeDiscoveryService handles peer discovery and CRDT synchronization
type NodeDiscoveryService struct {
	buddyNode     *PubSubMessages.BuddyNode
	knownPeers    map[peer.ID]*PeerInfo
	discoveryChan chan peer.ID
	stopChan      chan struct{}
}

// PeerInfo contains information about a discovered peer
type PeerInfo struct {
	PeerID      peer.ID
	LastSeen    time.Time
	IsConnected bool
	CRDTState   *Types.Controller
}

// NewNodeDiscoveryService creates a new node discovery service
func NewNodeDiscoveryService(buddyNode *PubSubMessages.BuddyNode) *NodeDiscoveryService {
	return &NodeDiscoveryService{
		buddyNode:     buddyNode,
		knownPeers:    make(map[peer.ID]*PeerInfo),
		discoveryChan: make(chan peer.ID, 100),
		stopChan:      make(chan struct{}),
	}
}
