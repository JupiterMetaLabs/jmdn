package Helper

import (
	Pubsub "gossipnode/Pubsub"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
)

func GPStoSGPS(GossipPubSubInput *Struct.GossipPubSub) *Pubsub.StructGossipPubSub {
	return &Pubsub.StructGossipPubSub{
		GossipPubSub: GossipPubSubInput,
	}
}
