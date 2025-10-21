package Service

import (
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
)

// ServiceManager coordinates all services
type ServiceManager struct {
	subscriptionService  *SubscriptionService
	consensusService     *ConsensusService
	publishService       *PublishService
	nodeDiscoveryService *NodeDiscoveryService
	validationService    *ValidationService
}

// NewServiceManager creates a new service manager with all services
func NewServiceManager(pubSub *Struct.GossipPubSub, buddyNode *Structs.BuddyNode) *ServiceManager {
	// Convert the buddyNode into struct
	return &ServiceManager{
		subscriptionService:  NewSubscriptionService(pubSub),
		consensusService:     NewConsensusService(buddyNode),
		publishService:       NewPublishService(buddyNode),
		nodeDiscoveryService: NewNodeDiscoveryService(buddyNode),
		validationService:    NewValidationService(),
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

// GetNodeDiscoveryService returns the node discovery service
func (sm *ServiceManager) GetNodeDiscoveryService() *NodeDiscoveryService {
	return sm.nodeDiscoveryService
}

// GetValidationService returns the validation service
func (sm *ServiceManager) GetValidationService() *ValidationService {
	return sm.validationService
}
