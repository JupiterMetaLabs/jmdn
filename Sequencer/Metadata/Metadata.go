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

func (cmr *ConsensusMetadataRouter) SetEndTimeoutMetadata(endTimeout time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetEndTimeout(endTimeout)
	return cmr
}

func (cmr *ConsensusMetadataRouter) SetStartTimeMetadata(startTime time.Time) *ConsensusMetadataRouter {
	cmr.ConsensusMessage.SetStartTime(startTime)
	return cmr
}

func (cmr *ConsensusMetadataRouter) GetConsensusMessage() *PubSubMessages.ConsensusMessage {
	return cmr.ConsensusMessage
}
