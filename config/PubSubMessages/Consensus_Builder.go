package PubSubMessages

import (
	"gossipnode/config"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
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

func (consensusMessage *ConsensusMessage) SetBuddies(buddies *Buddies) *ConsensusMessage {
	consensusMessage.Buddies = ConvertBuddiesIntoHashMap(buddies)
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetBuddies() map[int]peer.ID {
	return consensusMessage.Buddies
}

func (consensusMessage *ConsensusMessage) AddBuddies(buddies *Buddies) *ConsensusMessage {
	// Convert new buddies to HashMap and merge with existing
	newBuddiesMap := ConvertBuddiesIntoHashMap(buddies)
	for key, value := range newBuddiesMap {
		consensusMessage.Buddies[key] = value
	}
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) RemoveBuddies(buddies *Buddies) *ConsensusMessage {
	// Convert buddies to remove into HashMap and remove from existing
	buddiesToRemove := ConvertBuddiesIntoHashMap(buddies)
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

func (consensusMessage *ConsensusMessage) CheckTimeOut() bool {
	return time.Now().After(consensusMessage.GetEndTimeout())
}

func (consensusMessage *ConsensusMessage) SetStartTime(startTime time.Time) *ConsensusMessage {
	consensusMessage.StartTime = startTime
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetStartTime() time.Time {
	return consensusMessage.StartTime
}
