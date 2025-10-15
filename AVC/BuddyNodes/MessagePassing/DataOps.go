package MessagePassing

import (
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"strconv"
)

// This function will add the vote to the local CRDT Engine, if the flag is true it will propagate the vote to the pubsub network
func AddVote(buddy *BuddyNode, vote int8, propagate bool) error {
	// First add to the network
	if err := DataLayer.Add(buddy.CRDTLayer, buddy.PeerID, "votes", strconv.Itoa(int(vote))); err != nil {
		return err
	}
	if propagate {
		if pubsub, ok := buddy.PubSub.(*Pubsub.GossipPubSub); ok {
			pubsub.Publish(config.Type_SubmitVote, vote, nil, config.BuddyNodesMessageProtocol)
		}
	}
	return nil
}