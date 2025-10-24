package PubSubMessages

import (
	"gossipnode/config"
	"time"
)

type ConsensusMessage struct {
	ZKBlock *config.ZKBlock
	Buddies *Buddies
	EndTimeout time.Time
	StartTime time.Time
	InteriumTime time.Time
	TotalNodes int
}
