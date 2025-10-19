package DataProcessing

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessageParsing"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/protocol"
)

// Parse messages from the go pubsub channel buffer and submit to the message parsing in MessageParsing.go
type MessageProcessing struct{
	Protocol protocol.ID
}

func GetMessageProcessing(protocol protocol.ID) *MessageProcessing {
	return &MessageProcessing{
		Protocol: protocol,
	}
}

func (mp *MessageProcessing) ParseMessage(message string) error {
	switch mp.Protocol {
		case config.BuddyNodesMessageProtocol:
			return MessageParsing.Router(message)
		default:
			return fmt.Errorf("unknown protocol: %s", mp.Protocol)
	}
}