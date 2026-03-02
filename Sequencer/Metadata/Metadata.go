package Metadata

import (
	"time"

	"jmdn/config"
	"jmdn/config/PubSubMessages"
)

type ConsensusMetadataRouter struct {
	ConsensusMessage *PubSubMessages.ConsensusMessage
}

func ZKBlockMetadata(zkBlock *config.ZKBlock, buddies []PubSubMessages.Buddy_PeerMultiaddr) *ConsensusMetadataRouter {
	NewBlock := &ConsensusMetadataRouter{
		ConsensusMessage: PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(zkBlock).SetBuddies(buddies),
	}
	return NewBlock
}

func (cmr *ConsensusMetadataRouter) AddBuddieNodesMetadata(buddies []PubSubMessages.Buddy_PeerMultiaddr) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.AddBuddies(buddies)
	return cmr
}

func (cmr *ConsensusMetadataRouter) RemoveBuddieNodesMetadata(buddies []PubSubMessages.Buddy_PeerMultiaddr) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.RemoveBuddies(buddies)
	return cmr
}

func (cmr *ConsensusMetadataRouter) SetEndTimeoutMetadata(endTimeout time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetEndTimeout(endTimeout)
	return cmr
}

func (cmr *ConsensusMetadataRouter) SetStartTimeMetadata(startTime time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetStartTime(startTime)
	return cmr
}

func (cmr *ConsensusMetadataRouter) SetInteriumTimeMetadata(interiumTime time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetInteriumTime(interiumTime)
	return cmr
}

func (cmr *ConsensusMetadataRouter) GetConsensusMessage() *PubSubMessages.ConsensusMessage {
	return cmr.ConsensusMessage
}

func (cmr *ConsensusMetadataRouter) CheckForTimeout() bool {
	return cmr.ConsensusMessage.CheckTimeOut()
}

func (cmr *ConsensusMetadataRouter) GetEndTimeoutMetadata() time.Time {
	return cmr.ConsensusMessage.GetEndTimeout()
}

func (cmr *ConsensusMetadataRouter) GetStartTimeMetadata() time.Time {
	return cmr.ConsensusMessage.GetStartTime()
}
