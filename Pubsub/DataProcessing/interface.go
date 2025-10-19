package DataProcessing

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessageParsing"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/protocol"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
)

// Parse messages from the go pubsub channel buffer and submit to the message parsing in MessageParsing.go
type MessageProcessing struct{
	MessageProcessingStruct *Struct.MessageProcessing
}

func NewMessageProcessing() *MessageProcessing{
	return &MessageProcessing{
		MessageProcessingStruct: &Struct.MessageProcessing{},
	}
}

func (Data *MessageProcessing) setProtocol(protocol protocol.ID) {
	Data.MessageProcessingStruct.Protocol = protocol
}

func (Data *MessageProcessing) setMessage(message string) {
	Data.MessageProcessingStruct.GossipMessage = message
}


// < -- Get functions -->
func (Data *MessageProcessing) GetProtocol() protocol.ID {
	return Data.MessageProcessingStruct.Protocol
}

func (Data *MessageProcessing) GetMessage() string {
	return Data.MessageProcessingStruct.GossipMessage
}

// < -- Parse functions -->
func (Data *MessageProcessing) ParseMessage() error {
	switch Data.GetProtocol() {
		case config.BuddyNodesMessageProtocol:
			return MessageParsing.Router(Data.GetMessage())
		default:
			return fmt.Errorf("unknown protocol: %s", Data.GetProtocol())
	}
}