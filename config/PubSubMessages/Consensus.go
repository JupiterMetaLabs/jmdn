package PubSubMessages

import (
	"gossipnode/config"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type ConsensusMessage struct {
	ZKBlock      *config.ZKBlock
	Buddies      map[int]peer.ID
	EndTimeout   time.Time
	StartTime    time.Time
	InteriumTime time.Time
	TotalNodes   int
}

func ConvertBuddiesIntoHashMap(buddies *Buddies) map[int]peer.ID {
	hashMap := make(map[int]peer.ID)
	for i, buddy := range buddies.Buddies_Nodes {
		hashMap[i+1] = buddy // Start from 1, not 0
	}
	return hashMap
}
