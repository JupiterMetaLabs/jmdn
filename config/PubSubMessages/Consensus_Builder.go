package PubSubMessages

import (
	"gossipnode/config"
	"time"
)

func NewConsensusMessageBuilder(consensusMessage *ConsensusMessage) *ConsensusMessage {
	if consensusMessage != nil {
		return &ConsensusMessage{
			ZKBlock: consensusMessage.ZKBlock,
			Buddies: consensusMessage.Buddies,
			EndTimeout: consensusMessage.EndTimeout,
			StartTime: consensusMessage.StartTime,
			InteriumTime: consensusMessage.InteriumTime,
			TotalNodes: consensusMessage.TotalNodes,
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
	consensusMessage.Buddies = buddies
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) GetBuddies() *Buddies {
	return consensusMessage.Buddies
}

func (consensusMessage *ConsensusMessage) AddBuddies(buddies *Buddies) *ConsensusMessage {
	consensusMessage.Buddies.Buddies_Nodes = append(consensusMessage.Buddies.Buddies_Nodes, buddies.Buddies_Nodes...)
	return consensusMessage
}

func (consensusMessage *ConsensusMessage) RemoveBuddies(buddies *Buddies) *ConsensusMessage {
	consensusMessage.Buddies.Buddies_Nodes = removeBuddies(consensusMessage.Buddies.Buddies_Nodes, buddies.Buddies_Nodes)
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