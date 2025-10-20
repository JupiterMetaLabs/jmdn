package Service

import (
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/Pubsub"
)

// ServiceManager coordinates all services
type ServiceManager struct {
	subscriptionService *SubscriptionService
	consensusService    *ConsensusService
	publishService      *PublishService
}

// NewServiceManager creates a new service manager with all services
func NewServiceManager(pubSub *Pubsub.GossipPubSub, buddyNode *Structs.BuddyNode) *ServiceManager {
	// Convert the buddyNode into struct
	return &ServiceManager{
		subscriptionService: NewSubscriptionService(pubSub),
		consensusService:    NewConsensusService(buddyNode),
		publishService:      NewPublishService(buddyNode),
	}
}

// GetSubscriptionService returns the subscription service
func (sm *ServiceManager) GetSubscriptionService() *SubscriptionService {
	return sm.subscriptionService
}

// GetConsensusService returns the consensus service
func (sm *ServiceManager) GetConsensusService() *ConsensusService {
	return sm.consensusService
}

// GetPublishService returns the publish service
func (sm *ServiceManager) GetPublishService() *PublishService {
	return sm.publishService
}
