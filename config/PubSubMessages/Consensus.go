package PubSubMessages

import (
	"gossipnode/config"
	"time"

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
