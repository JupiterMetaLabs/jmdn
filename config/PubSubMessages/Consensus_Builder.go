package PubSubMessages

import (
	"time"

	"gossipnode/config"
)

func NewConsensusMessageBuilder(consensusMessage *ConsensusMessage) *ConsensusMessage {
	if consensusMessage != nil {
		return &ConsensusMessage{
			ZKBlock:      consensusMessage.ZKBlock,
			Buddies:      consensusMessage.Buddies,
			EndTimeout:   consensusMessage.EndTimeout,
			StartTime:    consensusMessage.StartTime,
			InteriumTime: consensusMessage.InteriumTime,
			TotalNodes:   consensusMessage.TotalNodes,
		}
	}
	return &ConsensusMessage{}
}

func (consensusMessage *ConsensusMessage) SetInteriumTime(interiumTime time.Time) *ConsensusMessage {
	consensusMessage.InteriumTime = interiumTime
	return consensusMessage
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) GetInteriumTime() time.Time {
	return consensusMessage.InteriumTime
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) SetTotalNodes(totalNodes int) *ConsensusMessage {
	consensusMessage.TotalNodes = totalNodes
	return consensusMessage
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) GetTotalNodes() int {
	return consensusMessage.TotalNodes
}

func (consensusMessage *ConsensusMessage) SetZKBlock(zkBlock *config.ZKBlock) *ConsensusMessage {
	consensusMessage.ZKBlock = zkBlock
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetZKBlock() *config.ZKBlock {
	return consensusMessage.ZKBlock
}

func (consensusMessage *ConsensusMessage) SetBuddies(buddies []Buddy_PeerMultiaddr) *ConsensusMessage {
	consensusMessage.Buddies = ConvertBuddiesIntoHashMap_PeerMultiaddr(buddies)
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetBuddies() map[int]Buddy_PeerMultiaddr {
	return consensusMessage.Buddies
}

func (consensusMessage *ConsensusMessage) AddBuddies(buddies []Buddy_PeerMultiaddr) *ConsensusMessage {
	// Convert new buddies to HashMap and merge with existing
	newBuddiesMap := ConvertBuddiesIntoHashMap_PeerMultiaddr(buddies)
	for key, value := range newBuddiesMap {
		consensusMessage.Buddies[key] = value
	}
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) RemoveBuddies(buddies []Buddy_PeerMultiaddr) *ConsensusMessage {
	// Convert buddies to remove into HashMap and remove from existing
	buddiesToRemove := ConvertBuddiesIntoHashMap_PeerMultiaddr(buddies)
	for key := range buddiesToRemove {
		delete(consensusMessage.Buddies, key)
	}
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) SetEndTimeout(endTimeout time.Time) *ConsensusMessage {
	consensusMessage.EndTimeout = endTimeout
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetEndTimeout() time.Time {
	return consensusMessage.EndTimeout
}

// Returns true if the consensus message has timed out
// Returns false if the consensus message has not timed out
func (consensusMessage *ConsensusMessage) CheckTimeOut() bool {
	return time.Now().UTC().After(consensusMessage.GetEndTimeout())
}

func (consensusMessage *ConsensusMessage) SetStartTime(startTime time.Time) *ConsensusMessage {
	consensusMessage.StartTime = startTime
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetStartTime() time.Time {
	return consensusMessage.StartTime
}

// MaxCachedConsensusMessages bounds cacheConsensuMessage: it has no
// caller-supplied authentication and previously had no eviction, so an
// attacker choosing an arbitrary block hash could otherwise grow it without
// limit. On overflow the entry with the HIGHEST BlockNumber is evicted, not
// the lowest: the handler that feeds this cache also enforces a tight
// ahead/behind window around the local tip before ever calling this setter,
// so the legitimate in-flight proposal is almost always the LOWEST height
// present (closest to the tip that just committed), while an attacker
// flooding the window has to reach for the window's upper edge to do it.
// Evicting the lowest would let that flood push the honest entry out first;
// evicting the highest instead confines an attacker to spamming at the exact
// same height as the honest proposal, which needs sender authentication to
// close fully (not yet built).
const MaxCachedConsensusMessages = 6

func (consensusMessage *ConsensusMessage) SetGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return consensusMessage // never key on a nil block (audit NET-01 deref)
	}
	key := consensusMessage.ZKBlock.BlockHash.String()
	cacheMu.Lock()
	if _, exists := cacheConsensuMessage[key]; !exists && len(cacheConsensuMessage) >= MaxCachedConsensusMessages {
		var worstKey string
		var worstHeight uint64
		first := true
		for k, v := range cacheConsensuMessage {
			h := uint64(0)
			if v != nil && v.ZKBlock != nil {
				h = v.ZKBlock.BlockNumber
			}
			if first || h > worstHeight {
				worstKey, worstHeight, first = k, h, false
			}
		}
		if worstKey != "" {
			delete(cacheConsensuMessage, worstKey)
		}
	}
	cacheConsensuMessage[key] = consensusMessage
	cacheMu.Unlock()
	return consensusMessage
}

// PruneConsensusMessagesBelow removes every cached entry whose ZKBlock height
// is strictly below minHeight. Callers use this to clear stale entries left
// over from an earlier round before a new one can be crowded out by them —
// a cache slot a stale entry still holds is one less slot available to the
// current round, with no eviction needed to reclaim it.
func PruneConsensusMessagesBelow(minHeight uint64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for k, v := range cacheConsensuMessage {
		if v == nil || v.ZKBlock == nil || v.ZKBlock.BlockNumber < minHeight {
			delete(cacheConsensuMessage, k)
		}
	}
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) GetGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return nil
	}
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return cacheConsensuMessage[consensusMessage.ZKBlock.BlockHash.String()]
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) RemoveGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return consensusMessage
	}
	cacheMu.Lock()
	delete(cacheConsensuMessage, consensusMessage.ZKBlock.BlockHash.String())
	cacheMu.Unlock()
	return consensusMessage
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (consensusMessage *ConsensusMessage) ClearGloalVarCacheConsensusMessage() *ConsensusMessage {
	cacheMu.Lock()
	cacheConsensuMessage = make(map[string]*ConsensusMessage)
	cacheMu.Unlock()
	return consensusMessage
}
