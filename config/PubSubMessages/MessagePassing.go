package PubSubMessages

import (
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/config"

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
	PubSub          *GossipPubSub // Will hold a reference to GossipPubSub instance
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

func NewBuddyNodeBuilder(buddy *BuddyNode) *BuddyNode {
	if buddy != nil {
		return &BuddyNode{
		CRDTLayer: buddy.CRDTLayer,
		Host: buddy.Host,
		Network: buddy.Network,
		PeerID: buddy.PeerID,
		BuddyNodes: buddy.BuddyNodes,
		Mutex: sync.RWMutex{},
		MetaData: buddy.MetaData,
		ResponseHandler: buddy.ResponseHandler,
		PubSub: buddy.PubSub,
		StreamCache: buddy.StreamCache,
		}
	}
	return &BuddyNode{}
}

func (buddy *BuddyNode) SetCRDTLayer(crdtlayer *Types.Controller) *BuddyNode {
	buddy.CRDTLayer = crdtlayer
	return buddy
}

func (buddy *BuddyNode) SetHost(host host.Host) *BuddyNode {
	buddy.Host = host
	return buddy
}

func (buddy *BuddyNode) SetNetwork(network network.Network) *BuddyNode {
	buddy.Network = network
	return buddy
}

func (buddy *BuddyNode) SetPeerID(peerID peer.ID) *BuddyNode {
	buddy.PeerID = peerID
	return buddy
}

func (buddy *BuddyNode) SetBuddyNodes(buddies Buddies) *BuddyNode {
	buddy.BuddyNodes = buddies
	return buddy
}

func (buddy *BuddyNode) AddBuddies(buddies []peer.ID) *BuddyNode {
	buddy.BuddyNodes.Buddies_Nodes = append(buddy.BuddyNodes.Buddies_Nodes, buddies...)
	return buddy
}

func (buddy *BuddyNode) RemoveBuddies(buddies []peer.ID) *BuddyNode {
	buddy.BuddyNodes.Buddies_Nodes = removeBuddies(buddy.BuddyNodes.Buddies_Nodes, buddies)
	return buddy
}

func removeBuddies(buddies []peer.ID, remove []peer.ID) []peer.ID {
	for _, remove := range remove {
		for i, buddy := range buddies {
			if buddy == remove {
				buddies = append(buddies[:i], buddies[i+1:]...)
			}
		}
	}
	return buddies
}

func (buddy *BuddyNode) SetResponseHandler(responseHandler ResponseHandler) *BuddyNode {
	buddy.ResponseHandler = responseHandler
	return buddy
}

func (buddy *BuddyNode) SetPubSub(pubsub *GossipPubSub) *BuddyNode {
	buddy.PubSub = pubsub
	return buddy
}

func (buddy *BuddyNode) SetStreamCache(streamCache *StreamCache) *BuddyNode {
	buddy.StreamCache = NewStreamCacheBuilder(streamCache)
	return buddy
}

func (buddy *BuddyNode) GetStreamCache() *StreamCache {
	return NewStreamCacheBuilder(buddy.StreamCache)
}

func (buddy *BuddyNode) GetPubSub() *GossipPubSub {
	return NewGossipPubSubBuilder(buddy.PubSub)
}

func (buddy *BuddyNode) GetCRDTLayer() *Types.Controller {
	return buddy.CRDTLayer
}

func (buddy *BuddyNode) GetHost() host.Host {
	return buddy.Host
}

func (buddy *BuddyNode) GetNetwork() network.Network {
	return buddy.Network
}

func (buddy *BuddyNode) GetPeerID() peer.ID {
	return buddy.PeerID
}

func (buddy *BuddyNode) GetBuddyNodes() Buddies {
	return buddy.BuddyNodes
}

func (buddy *BuddyNode) GetResponseHandler() ResponseHandler {
	return buddy.ResponseHandler
}

func (buddy *BuddyNode) GetMetaData() MetaData {
	return buddy.MetaData
}

func (buddy *BuddyNode) IsAllowed(peerID peer.ID) bool {
	if buddy.PubSub == nil {
		return false
	}
	access, exists := buddy.PubSub.ChannelAccess[config.PubSub_ConsensusChannel]
	if !exists {
		return false
	}
	return access.AllowedPeers[peerID]
}

func (buddy *BuddyNode) IsPublicChannel(peerID peer.ID) bool {

	return buddy.PubSub.ChannelAccess[config.PubSub_ConsensusChannel].IsPublic
}