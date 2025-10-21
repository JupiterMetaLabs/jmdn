package Router

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/config"
)

// Router routes messages to appropriate services based on message type
// This function acts as a simple router that delegates to service layer
func Router(message string) error {
	// Convert the message into Pubsub.GossipMessage
	gossipMessage, err := ConvertMessage(message)
	if err != nil {
		return fmt.Errorf("failed to convert message: %v", err)
	}
	// Create service manager with dependencies
	GossipNode := Structs.NewGlobalVariables().Get_PubSubNode()
	PubSub := GossipNode.PubSub
	serviceManager := Service.NewServiceManager(PubSub, GossipNode)

	// Route to appropriate services based on the message ack type
	switch gossipMessage.Data.ACK.Stage {
	case config.Type_AskForSubscription:
		return serviceManager.GetSubscriptionService().HandleAskForSubscription(gossipMessage)
	case config.Type_VerifySubscription:
		return serviceManager.GetConsensusService().HandleVerifySubscription(gossipMessage)
	case config.Type_EndPubSub:
		return serviceManager.GetSubscriptionService().HandleEndPubSub(gossipMessage)
	case config.Type_Publish:
		return serviceManager.GetPublishService().HandlePublish(gossipMessage)
	default:
		return fmt.Errorf("unknown stage: %s", gossipMessage.Data.ACK.Stage)
	}
}
