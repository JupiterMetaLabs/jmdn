package Router

import (
	"encoding/json"
	"fmt"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
)

// Take the input as a json string and return the message as a Pubsub.GossipMessage and also convert GossipMessage.Data from interface to MessagePassing.Message
func ConvertMessage(message string) (*Struct.GossipMessage, error) {
	var gossipMessage Struct.GossipMessage
	if err := json.Unmarshal([]byte(message), &gossipMessage); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %v", err)
	}

	return &gossipMessage, nil
}