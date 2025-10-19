package MessageParsing

import (
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/config"
)

// Router for the message parsing
func Router(message string) error{
	// Convert the message into Pubsub.GossipMessage
	gossipMessage, err := ConvertMessage(message)
	if err != nil {
		return fmt.Errorf("failed to convert message: %v", err)
	}
	// Router to the approprite funcitons based on the message ack type
	switch gossipMessage.Data.ACK.Stage {
	case config.Type_VerifySubscription:
		return HandleVerifySubscription(gossipMessage)
	default:
		return fmt.Errorf("unknown stage: %s", gossipMessage.Data.ACK.Stage)
	}

	// <-- TODO: parse the messages --> 
}

func HandleVerifySubscription(gossipMessage *Pubsub.GossipMessage) error {
	return nil
}