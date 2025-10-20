package DataProcessing

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessageParsing"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
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

func (Data *MessageProcessing) SetProtocol(protocol protocol.ID) *MessageProcessing {
	Data.MessageProcessingStruct.Protocol = protocol
	return Data
}

func (Data *MessageProcessing) SetMessage(message string) *MessageProcessing {
	Data.MessageProcessingStruct.GossipMessage = message
	return Data
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
			pubSub := MessagePassing.NewGlobalVariables().Get_PubSubNode().GetPubSub()
			return MessageParsing.Router(Data.GetMessage(), pubSub)
		default:
			return fmt.Errorf("unknown protocol: %s", Data.GetProtocol())
	}
}