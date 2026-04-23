package helper

import (
	"fmt"
	"time"

	"gossipnode/Sequencer/Metadata"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/config/PubSubMessages/Cache"
)

// @static function
/*
This function initializes the candidate lists for the consensus.
What it does:
Creates two slices: MainCandidates and BackupCandidates
Returns the two slices
*/
func InitCandidateLists(size int) ([]PubSubMessages.Buddy_PeerMultiaddr, []PubSubMessages.Buddy_PeerMultiaddr) {
	MainCandidates := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, size)
	BackupCandidates := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, size)
	return MainCandidates, BackupCandidates
}

// @static function
/*
This function adds buddy nodes to the cache temporarily.
What it does:
Calls Cache.AddPeersTemporary(buddies) to add the buddy nodes to the cache.
*/
func AddBuddyNodesTemporarily(buddies []PubSubMessages.Buddy_PeerMultiaddr) Cache.Stats {
	return Cache.AddPeersTemporary(buddies)
}

// @static function
// PingAndAddToCache pings a buddy node and adds it to cache if reachable
// Does NOT connect - connection is handled separately by AddPeerCache.ConnectToTemporaryPeers
func PingAndAddToCache(buddy PubSubMessages.Buddy_PeerMultiaddr) error {
	nodeManager := Cache.GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	addrStr := buddy.Multiaddr.String()

	// Ping to check reachability
	reachable, _, err := nodeManager.PingMultiaddrWithRetries(addrStr, 3)
	if err != nil {
		return fmt.Errorf("ping failed: %v", err)
	}

	if !reachable {
		return fmt.Errorf("peer not reachable at %s", addrStr)
	}

	// Add to cache (connection will be done by AddPeerCache.ConnectToTemporaryPeers)
	Cache.AddPeer(buddy.PeerID, buddy.Multiaddr)

	return nil
}

// @static function
/*
This function wraps a ZKBlock and buddy peers into a ConsensusMessage with timing metadata.
What it does:
Creates metadata: Calls Metadata.ZKBlockMetadata(zkBlock, buddies) to build a ConsensusMetadataRouter containing a ConsensusMessage with the ZKBlock and buddy peers.
Sets timing:
End timeout: time.Now().UTC().Add(config.ConsensusTimeout)
Start time: time.Now().UTC()
Returns the message: Extracts and returns the ConsensusMessage from the metadata router
*/
func AddBuddyNodesToPeerList(zkBlock *config.ZKBlock, buddies []PubSubMessages.Buddy_PeerMultiaddr) (*PubSubMessages.ConsensusMessage, error) {

	ZKBlock := Metadata.ZKBlockMetadata(zkBlock, buddies).SetEndTimeoutMetadata(time.Now().UTC().Add(config.ConsensusTimeout)).SetStartTimeMetadata(time.Now().UTC())
	if ZKBlock == nil {
		return nil, fmt.Errorf("failed to create ZKBlock metadata")
	}
	return ZKBlock.GetConsensusMessage(), nil
}
