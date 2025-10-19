package MessageParsing

import (
	"encoding/json"
	"fmt"
	"gossipnode/Pubsub"
)

// Take the input as a json string and return the message as a Pubsub.GossipMessage and also convert GossipMessage.Data from interface to MessagePassing.Message
func ConvertMessage(message string) (*Pubsub.GossipMessage, error) {
	var gossipMessage Pubsub.GossipMessage
	if err := json.Unmarshal([]byte(message), &gossipMessage); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %v", err)
	}

	return &gossipMessage, nil
}