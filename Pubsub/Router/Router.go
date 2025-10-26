package Router

import (
	"fmt"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
)

// Router routes messages to appropriate services based on message type
// This function acts as a simple router that delegates to service layer
func Router(message *AVCStruct.GossipMessage) error {
	// Convert the message into Pubsub.GossipMessage
	// Create service manager with dependencies
	GossipNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	PubSub := GossipNode.PubSub
	serviceManager := NewServiceManager(PubSub, GossipNode)
	fmt.Println("Router", message.Data.ACK.Stage)
	fmt.Println("message", message)


	// Route to appropriate services based on the message ack type
	switch message.Data.ACK.Stage {
	case config.Type_AskForSubscription:

		return serviceManager.GetSubscriptionService().HandleAskForSubscription(message)
	case config.Type_VerifySubscription:
		return serviceManager.GetConsensusService().HandleVerifySubscription(message)
	case config.Type_EndPubSub:
		return serviceManager.GetSubscriptionService().HandleEndPubSub(message)
	// case config.Type_Publish:
	// 	return serviceManager.GetPublishService().HandlePublish(message)
	case config.Type_ToBeProcessed:
		// TODO not implemented yet
		return serviceManager.GetPublishService().HandlePublish(message)
	default:
		return fmt.Errorf("unknown stage: %s", message.Data.ACK.Stage)
	}
}
