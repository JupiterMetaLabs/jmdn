package Struct

import (

	"github.com/libp2p/go-libp2p/core/protocol"
)

type MessageProcessing struct {
	GossipMessage string
	Protocol protocol.ID
}