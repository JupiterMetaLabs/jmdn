package PubSubMessages

import (
	"sync"
	"time"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// CacheConsensuMessage is the process-wide consensus-message cache keyed by
// block hash. It is written on the gossip/consensus path and ranged over from
// several other packages (CRDT sync, subscription service, listener). All
// access MUST go through the accessors below — a bare map range concurrent
// with a write is a Go runtime throw (uncatchable by recover), not a panic
// (audit NET-01). cacheMu guards every read, write and iteration.
var (
	cacheMu              sync.RWMutex
	CacheConsensuMessage = make(map[string]*ConsensusMessage)
)

// SnapshotConsensusMessages returns a shallow copy of the cache for safe
// iteration. Callers range over the returned map, never CacheConsensuMessage
// directly. The values are shared pointers — treat them read-only.
func SnapshotConsensusMessages() map[string]*ConsensusMessage {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	out := make(map[string]*ConsensusMessage, len(CacheConsensuMessage))
	for k, v := range CacheConsensuMessage {
		out[k] = v
	}
	return out
}

type ConsensusMessage struct {
	ZKBlock      *config.ZKBlock
	Buddies      map[int]Buddy_PeerMultiaddr
	EndTimeout   time.Time
	StartTime    time.Time
	InteriumTime time.Time
	TotalNodes   int
	SequencerID  string
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
