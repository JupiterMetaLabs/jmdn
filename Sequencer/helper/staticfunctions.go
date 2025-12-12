package helper

import (
	"fmt"
	"gossipnode/AVC/NodeSelection/Router"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// @static function

// With the block you will attack the metadata to it before the propagation of the block
// QUERY BUDDY NODES FUNCTIONALITY (we would need this to get the buddy nodes prompted)
func QueryBuddyNodes() ([]PubSubMessages.Buddy_PeerMultiaddr, error) {
	router := Router.NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(config.MaxMainPeers + config.MaxBackupPeers)
	if err != nil {
		return nil, fmt.Errorf("failed to get buddy nodes: %v", err)
	}

	GetPeerIDFromBuddy, errMSG := router.GetBuddyNodesFromList(buddies)
	if errMSG != nil {
		return nil, fmt.Errorf("failed to get buddy nodes from list: %v", errMSG)
	}

	// fmt.Printf("Queried Buddies: %+v\n", GetPeerIDFromBuddy)
	return GetPeerIDFromBuddy, nil
}

// #static function

/*
This function extracts unique peer IDs from a slice of buddy peers.
What it does:
Takes a slice of Buddy_PeerMultiaddr objects (buddies)
Extracts unique peer IDs from them, avoiding duplicates
Returns a slice of unique peer.ID values
*/
func GetUniquePeerIDs(buddies []PubSubMessages.Buddy_PeerMultiaddr) ([]peer.ID, error) {
	peerIDs := make([]peer.ID, 0)
	seenPeers := make(map[string]bool) // Track seen peer IDs to avoid duplicates

	for _, buddy := range buddies {
		peerIDStr := buddy.PeerID.String()
		if !seenPeers[peerIDStr] {
			peerIDs = append(peerIDs, buddy.PeerID)
			seenPeers[peerIDStr] = true
		}
	}
	return peerIDs, nil
}

// #static function
/*
This function deduplicates buddy peers by peer ID, keeping the full Buddy_PeerMultiaddr objects.
What it does:
Takes a slice of Buddy_PeerMultiaddr objects (buddies)
Removes duplicates based on peer.ID, keeping the first occurrence
Returns a slice of unique Buddy_PeerMultiaddr objects
*/
func GetUniqueBuddyPeers(buddies []PubSubMessages.Buddy_PeerMultiaddr) []PubSubMessages.Buddy_PeerMultiaddr {
	uniquePeers := make([]PubSubMessages.Buddy_PeerMultiaddr, 0)
	seenPeers := make(map[string]bool) // Track seen peer IDs to avoid duplicates

	for _, buddy := range buddies {
		peerIDStr := buddy.PeerID.String()
		if !seenPeers[peerIDStr] {
			uniquePeers = append(uniquePeers, buddy)
			seenPeers[peerIDStr] = true
		}
	}
	return uniquePeers
}

// @static function
/*
This function converts a map of peer.ID multiaddr.Multiaddr to a slice of Buddy_PeerMultiaddr.
What it does:
Takes a map of peer.ID to multiaddr.Multiaddr (reachablePeers)
Converts it to a slice of Buddy_PeerMultiaddr
Returns a slice of Buddy_PeerMultiaddr
*/
func ConvertMapToSlice(reachablePeers map[peer.ID]multiaddr.Multiaddr) []PubSubMessages.Buddy_PeerMultiaddr {
	reachablePeersSlice := make([]PubSubMessages.Buddy_PeerMultiaddr, 0)
	for peerID, multiaddr := range reachablePeers {
		reachablePeersSlice = append(reachablePeersSlice, PubSubMessages.Buddy_PeerMultiaddr{
			PeerID:    peerID,
			Multiaddr: multiaddr,
		})
	}
	return reachablePeersSlice
}