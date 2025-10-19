package MessageParsing

import (
	"encoding/json"
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/config"
)

// Router for the message parsing
func Router(message string) error{
	// Convert the message into Pubsub.GossipMessage
	var pubsubMessage Pubsub.GossipMessage
	if err := json.Unmarshal([]byte(message), &pubsubMessage); err != nil {
		return fmt.Errorf("failed to unmarshal message: %v", err)
	}

	// <-- TODO: parse the messages --> 
	return nil
}