package DataProcessing

import (
	"encoding/json"
	"fmt"
	Router "gossipnode/Pubsub/Router"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/protocol"
)

// Parse messages from the go pubsub channel buffer and submit to the message parsing in MessageParsing.go
type MessageProcessing struct {
	MessageProcessingStruct *PubSubMessages.MessageProcessing
}

func NewMessageProcessing() *MessageProcessing {
	return &MessageProcessing{
		MessageProcessingStruct: &PubSubMessages.MessageProcessing{},
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

func (Data *MessageProcessing) ConvertStringToMessageProcessing(message string) *MessageProcessing {
	return &MessageProcessing{
		MessageProcessingStruct: &PubSubMessages.MessageProcessing{
			GossipMessage: message,
			Protocol:      Data.GetProtocol(),
		},
	}
}

// < -- Parse functions -->
func (Data *MessageProcessing) ParseMessage() error {
	switch Data.GetProtocol() {
	case config.BuddyNodesMessageProtocol:
		return Router.Router(Data.GetMessage())
	default:
		return fmt.Errorf("unknown protocol: %s", Data.GetProtocol())
	}
}

// <-- Helper functions -->
func ConvertInterfacetoStructMessageProcessing(message interface{}) (*PubSubMessages.MessageProcessing, error) {
	// COnvert interface{} to Struct.MessageProcessing
	switch message := message.(type) {
	case string:
		return ConvertStringtoStructMessageProcessing(message)
	case *PubSubMessages.MessageProcessing:
		return message, nil
	case PubSubMessages.MessageProcessing:
		return &message, nil
	default:
		return nil, fmt.Errorf("unknown type: %T", message)
	}
}

func ConvertStringtoStructMessageProcessing(message string) (*PubSubMessages.MessageProcessing, error) {
	var messageProcessing PubSubMessages.MessageProcessing
	if err := json.Unmarshal([]byte(message), &messageProcessing); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %v", err)
	}
	return &messageProcessing, nil
}
