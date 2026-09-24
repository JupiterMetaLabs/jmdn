package PubSubMessages

import (
	"sync"
	"time"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// cacheConsensuMessage is the process-wide consensus-message cache keyed by
// block hash. It is written on the gossip/consensus path and ranged over from
// several other packages (CRDT sync, subscription service, listener). All
// access MUST go through the accessors below — a bare map range concurrent
// with a write is a Go runtime throw (uncatchable by recover), not a panic
// (audit NET-01). cacheMu guards every read, write and iteration.
var (
	cacheMu              sync.RWMutex
	cacheConsensuMessage = make(map[string]*ConsensusMessage)
)

// SnapshotConsensusMessages returns a shallow copy of the cache for safe
// iteration. Callers range over the returned map, never cacheConsensuMessage
// directly. The values are shared pointers — treat them read-only.
func SnapshotConsensusMessages() map[string]*ConsensusMessage {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	out := make(map[string]*ConsensusMessage, len(cacheConsensuMessage))
	for k, v := range cacheConsensuMessage {
		out[k] = v
	}
	return out
}

// LookupConsensusMessageByBlockHash returns the cached consensus message for a
// block hash, or nil when this node has never seen that block.
//
// Exists for the vote-result signing path (audit D-26c): a buddy must resolve
// the block it is about to BLS-sign for from ITS OWN state, never from the
// requester's payload. SnapshotConsensusMessages would also work but copies the
// whole map per call, and this cache has no eviction in production
// (Remove/Clear are both dead code), so that copy grows without bound. This is
// the O(1) read.
//
// The returned pointer is shared — treat it read-only, exactly as with
// SnapshotConsensusMessages.
func LookupConsensusMessageByBlockHash(blockHash string) *ConsensusMessage {
	if blockHash == "" {
		return nil
	}
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return cacheConsensuMessage[blockHash]
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
