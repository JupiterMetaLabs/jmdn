package PubSubMessages

import (
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (buddy *BuddyNode) AddBuddies(buddies []peer.ID) *BuddyNode {
	buddy.BuddyNodes = *NewBuddiesBuilder(buddies)
	return buddy
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
