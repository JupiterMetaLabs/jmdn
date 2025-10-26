package Router

import (
	Service "gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	PubSubConnector "gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"
	AVCStruct "gossipnode/config/PubSubMessages"
)

// ServiceManager coordinates all services
type ServiceManager struct {
	subscriptionService  *PubSubConnector.SubscriptionService
	consensusService     *Service.VerificationService
	publishService       *Service.PublishService
	nodeDiscoveryService *Service.NodeDiscoveryService
	validationService    *Service.ValidationService
}

// NewServiceManager creates a new service manager with all services
func NewServiceManager(pubSub *AVCStruct.GossipPubSub, buddyNode *AVCStruct.BuddyNode) *ServiceManager {
	// Validate inputs
	if pubSub == nil {
		panic("NewServiceManager: pubSub cannot be nil")
	}
	if buddyNode == nil {
		panic("NewServiceManager: buddyNode cannot be nil")
	}

	// Convert the buddyNode into struct
	return &ServiceManager{
		subscriptionService:  PubSubConnector.NewSubscriptionService(pubSub),
		consensusService:     Service.NewVerificationService(buddyNode),
		publishService:       Service.NewPublishService(buddyNode),
		nodeDiscoveryService: Service.NewNodeDiscoveryService(buddyNode),
		validationService:    Service.NewValidationService(),
	}
}

// GetSubscriptionService returns the subscription service
func (sm *ServiceManager) GetSubscriptionService() *PubSubConnector.SubscriptionService {
	return sm.subscriptionService
}

// GetConsensusService returns the consensus service
func (sm *ServiceManager) GetConsensusService() *Service.VerificationService {
	return sm.consensusService
}

// GetPublishService returns the publish service
func (sm *ServiceManager) GetPublishService() *Service.PublishService {
	return sm.publishService
}

// GetNodeDiscoveryService returns the node discovery service
func (sm *ServiceManager) GetNodeDiscoveryService() *Service.NodeDiscoveryService {
	return sm.nodeDiscoveryService
}

// GetValidationService returns the validation service
func (sm *ServiceManager) GetValidationService() *Service.ValidationService {
	return sm.validationService
}
