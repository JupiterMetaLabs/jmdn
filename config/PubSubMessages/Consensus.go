package PubSubMessages

import (
	"time"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var CacheConsensuMessage = make(map[string]*ConsensusMessage)

type ConsensusMessage struct {
	ZKBlock      *config.ZKBlock
	Buddies      map[int]Buddy_PeerMultiaddr
	EndTimeout   time.Time
	StartTime    time.Time
	InteriumTime time.Time
	TotalNodes   int
	SequencerID  string // Peer ID of the consensus creator (sequencer) so voters know where to send votes
	RoundID      string // Unique round identifier (block hash) for vote scoping
}

type Buddy_PeerMultiaddr struct {
	PeerID    peer.ID
	Multiaddr multiaddr.Multiaddr
}

func ConvertBuddiesIntoHashMap_PeerMultiaddr(buddies []Buddy_PeerMultiaddr) map[int]Buddy_PeerMultiaddr {
	hashMap := make(map[int]Buddy_PeerMultiaddr)
	for i, buddy := range buddies {
		hashMap[i] = buddy
	}
	return hashMap
}
