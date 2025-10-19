package MessageParsing

import (
	"encoding/json"
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
)

// Take the input as a json string and return the message as a Pubsub.GossipMessage and also convert GossipMessage.Data from interface to MessagePassing.Message
func ConvertMessage(message string) (*Pubsub.GossipMessage, *MessagePassing.Message, error) {
	var gossipMessage Pubsub.GossipMessage
	if err := json.Unmarshal([]byte(message), &gossipMessage); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal message: %v", err)
	}

	var Message *MessagePassing.Message
	// Convert interface{} to MessagePassing.Message
	Message = MessagePassing.NewMessageBuilder().SetMessage(gossipMessage.Data.(string))
	return &gossipMessage, Message, nil
}