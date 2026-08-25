package Metadata

import (
	"time"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
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

// __DEAD_CODE_AUDIT_PUBLIC__
func (cmr *ConsensusMetadataRouter) AddBuddieNodesMetadata(buddies []PubSubMessages.Buddy_PeerMultiaddr) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.AddBuddies(buddies)
	return cmr
}

// __DEAD_CODE_AUDIT_PUBLIC__
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

// __DEAD_CODE_AUDIT_PUBLIC__
func (cmr *ConsensusMetadataRouter) SetInteriumTimeMetadata(interiumTime time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetInteriumTime(interiumTime)
	return cmr
}

func (cmr *ConsensusMetadataRouter) GetConsensusMessage() *PubSubMessages.ConsensusMessage {
	return cmr.ConsensusMessage
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (cmr *ConsensusMetadataRouter) CheckForTimeout() bool {
	return cmr.ConsensusMessage.CheckTimeOut()
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (cmr *ConsensusMetadataRouter) GetEndTimeoutMetadata() time.Time {
	return cmr.ConsensusMessage.GetEndTimeout()
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (cmr *ConsensusMetadataRouter) GetStartTimeMetadata() time.Time {
	return cmr.ConsensusMessage.GetStartTime()
}
