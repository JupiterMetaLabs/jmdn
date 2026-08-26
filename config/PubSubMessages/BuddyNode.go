package PubSubMessages

import (
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/Types"

	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

var PubSub_BuddyNode *BuddyNode
var ForListner *BuddyNode

// ResponseHandler interface for handling ACK responses
type ResponseHandler interface {
	HandleResponse(peerID peer.ID, accepted bool, role string)
}

type BuddyNode struct {
	CRDTLayer *Types.Controller

	// VoteCRDTLayer is the block-keyed vote CRDT (avc/crdt/votes), standing
	// alongside CRDTLayer during the migration described in
	// docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md. Stage 1: constructed and
	// reachable, nothing reads or writes it yet. CRDTLayer stays the
	// authoritative vote store through Stage 6 — do not remove it here.
	VoteCRDTLayer   *avctypes.Controller
	Host            host.Host
	Network         network.Network
	PeerID          peer.ID
	BuddyNodes      Buddies
	Mutex           sync.RWMutex
	MetaData        MetaData
	ResponseHandler ResponseHandler // Interface for handling responses
	PubSub          *GossipPubSub   // Will hold a reference to GossipPubSub instance
	StreamCache     *StreamCache    // LRU cache of reusable streams with TTL
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

func NewBuddiesBuilder(buddies []peer.ID) *Buddies {
	if buddies != nil {
		return &Buddies{
			Buddies_Nodes: buddies,
		}
	}
	return &Buddies{}
}

func (buddies *Buddies) AddBuddies(buddyNodes []peer.ID) *Buddies {
	buddies.Buddies_Nodes = append(buddies.Buddies_Nodes, buddyNodes...)
	return buddies
}

func (buddies *Buddies) RemoveBuddies(buddyNodes []peer.ID) *Buddies {
	buddies.Buddies_Nodes = removeBuddies(buddies.Buddies_Nodes, buddyNodes)
	return buddies
}

func (buddies *Buddies) GetBuddies() []peer.ID {
	return buddies.Buddies_Nodes
}
