package Router

import (
	"context"
	"fmt"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
)

// Router routes messages to appropriate services based on message type
// This function acts as a simple router that delegates to service layer
func Router(message *AVCStruct.GossipMessage) error {
	// Validate message structure
	if message == nil {
		return fmt.Errorf("message is nil")
	}
	if message.Data == nil {
		return fmt.Errorf("message data is nil")
	}
	if message.Data.ACK == nil {
		return fmt.Errorf("message ACK is nil")
	}

	logger_ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Convert the message into Pubsub.GossipMessage
	// Create service manager with dependencies
	globalVars := AVCStruct.NewGlobalVariables()
	if !globalVars.IsPubSubNodeInitialized() {
		return fmt.Errorf("PubSub node not properly initialized - call Set_PubSubNode() with valid BuddyNode first")
	}

	GossipNode := globalVars.Get_PubSubNode()
	if GossipNode == nil {
		return fmt.Errorf("GossipNode is nil - buddy node not initialized")
	}
	PubSub := GossipNode.PubSub
	serviceManager := NewServiceManager(PubSub, GossipNode)
	logger().Debug(logger_ctx, "Router: Processing message",
		ion.String("stage", message.Data.ACK.Stage),
		ion.String("sender", message.Sender.String()))
	logger().Debug(logger_ctx, "Router: GossipNode info",
		ion.String("peer_id", GossipNode.PeerID.String()),
		ion.String("host", PubSub.Host.ID().String()))

	// Route to appropriate services based on the message ack type
	switch message.Data.ACK.Stage {
	case config.Type_AskForSubscription:
		return serviceManager.GetSubscriptionService().HandleAskForSubscription(message)
	case config.Type_VerifySubscription:
		return serviceManager.GetConsensusService().HandleVerifySubscription(logger_ctx, message)
	case config.Type_EndPubSub:
		return serviceManager.GetSubscriptionService().HandleEndPubSub(message)
	// case config.Type_Publish:
	// 	return serviceManager.GetPublishService().HandlePublish(message)
	case config.Type_ToBeProcessed:
		// TODO not implemented yet
		return serviceManager.GetPublishService().HandlePublish(logger_ctx, message)
	default:
		return fmt.Errorf("unknown stage: %s", message.Data.ACK.Stage)
	}
}
