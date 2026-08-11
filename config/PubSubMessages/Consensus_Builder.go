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

func (consensusMessage *ConsensusMessage) GetInteriumTime() time.Time {
	return consensusMessage.InteriumTime
}

func (consensusMessage *ConsensusMessage) SetTotalNodes(totalNodes int) *ConsensusMessage {
	consensusMessage.TotalNodes = totalNodes
	return consensusMessage
}

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

func (consensusMessage *ConsensusMessage) SetGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return consensusMessage // never key on a nil block (audit NET-01 deref)
	}
	cacheMu.Lock()
	CacheConsensuMessage[consensusMessage.ZKBlock.BlockHash.String()] = consensusMessage
	cacheMu.Unlock()
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return nil
	}
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return CacheConsensuMessage[consensusMessage.ZKBlock.BlockHash.String()]
}

func (consensusMessage *ConsensusMessage) RemoveGloalVarCacheConsensusMessage() *ConsensusMessage {
	if consensusMessage.ZKBlock == nil {
		return consensusMessage
	}
	cacheMu.Lock()
	delete(CacheConsensuMessage, consensusMessage.ZKBlock.BlockHash.String())
	cacheMu.Unlock()
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) ClearGloalVarCacheConsensusMessage() *ConsensusMessage {
	cacheMu.Lock()
	CacheConsensuMessage = make(map[string]*ConsensusMessage)
	cacheMu.Unlock()
	return consensusMessage
}
