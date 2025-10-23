package Router

import (
	"fmt"
	Service "gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Router handles all verification functions from VerificationService
type Router struct {
	verificationService *Service.VerificationService
}

func (r *Router) Close() {
	// Clean up resources held by verificationService, if necessary
	// Currently, VerificationService does not require explicit cleanup,
	// but this method is here for future extensibility.
	r.verificationService = nil
}

// NewRouter creates a new router instance
func NewRouter(buddyNode *PubSubMessages.BuddyNode) *Router {
	return &Router{
		verificationService: Service.NewVerificationService(buddyNode),
	}
}

// HandleVerifySubscription routes verification subscription requests
func (r *Router) HandleVerifySubscription(gossipMessage *PubSubMessages.GossipMessage) error {
	if r.verificationService == nil {
		return fmt.Errorf("verification service not initialized")
	}

	return r.verificationService.HandleVerifySubscription(gossipMessage)
}

// VerifySubscriptions routes bulk subscription verification
func (r *Router) VerifySubscriptions(expectedPeers []peer.ID, timeout time.Duration) (map[peer.ID]string, error) {
	if r.verificationService == nil {
		return nil, fmt.Errorf("verification service not initialized")
	}

	return r.verificationService.VerifySubscriptions(expectedPeers, timeout)
}

// IsSubscribedToConsensusChannel routes subscription status check
func (r *Router) IsSubscribedToConsensusChannel() bool {
	if r.verificationService == nil {
		return false
	}

	return r.verificationService.IsSubscribedToConsensusChannel()
}

// SendVerificationResponse routes verification response sending
func (r *Router) SendVerificationResponse(accepted bool) error {
	if r.verificationService == nil {
		return fmt.Errorf("verification service not initialized")
	}

	return r.verificationService.SendVerificationResponse(accepted)
}

// GetVerificationStats routes verification statistics retrieval
func (r *Router) GetVerificationStats() map[string]interface{} {
	if r.verificationService == nil {
		return map[string]interface{}{
			"error": "verification service not initialized",
		}
	}

	return r.verificationService.GetVerificationStats()
}

// RouteMessage routes incoming messages to appropriate verification handlers
func (r *Router) RouteMessage(gossipMessage *PubSubMessages.GossipMessage) error {
	if gossipMessage == nil || gossipMessage.Data == nil || gossipMessage.Data.ACK == nil {
		return fmt.Errorf("invalid message structure")
	}

	switch gossipMessage.Data.ACK.Stage {
	case "Type_VerifySubscription":
		return r.HandleVerifySubscription(gossipMessage)
	default:
		return fmt.Errorf("unsupported message stage for verification: %s", gossipMessage.Data.ACK.Stage)
	}
}

// GetVerificationService returns the underlying verification service
func (r *Router) GetVerificationService() *Service.VerificationService {
	return r.verificationService
}

// SetVerificationService sets a new verification service
func (r *Router) SetVerificationService(service *Service.VerificationService) {
	r.verificationService = service
}

// ProcessVerificationRequest processes a verification request with full error handling
func (r *Router) ProcessVerificationRequest(gossipMessage *PubSubMessages.GossipMessage) error {
	if gossipMessage == nil {
		return fmt.Errorf("gossip message is nil")
	}

	if gossipMessage.Data == nil {
		return fmt.Errorf("message data is nil")
	}

	if gossipMessage.Data.ACK == nil {
		return fmt.Errorf("message ACK is nil")
	}

	// Route to appropriate handler based on message type
	return r.RouteMessage(gossipMessage)
}

// GetVerificationServiceStatus returns the status of the verification service
func (r *Router) GetVerificationServiceStatus() map[string]interface{} {
	status := map[string]interface{}{
		"service_initialized": r.verificationService != nil,
	}

	if r.verificationService != nil {
		// Get additional stats from the verification service
		stats := r.GetVerificationStats()
		status["verification_stats"] = stats
	}

	return status
}

// ValidateMessage validates a gossip message before processing
func (r *Router) ValidateMessage(gossipMessage *PubSubMessages.GossipMessage) error {
	if gossipMessage == nil {
		return fmt.Errorf("gossip message cannot be nil")
	}

	if gossipMessage.Data == nil {
		return fmt.Errorf("message data cannot be nil")
	}

	if gossipMessage.Data.ACK == nil {
		return fmt.Errorf("message ACK cannot be nil")
	}

	if gossipMessage.Sender == "" {
		return fmt.Errorf("message sender cannot be empty")
	}

	return nil
}

// ProcessCompleteVerification processes a complete verification workflow
func (r *Router) ProcessCompleteVerification(gossipMessage *PubSubMessages.GossipMessage) error {
	// Validate message first
	if err := r.ValidateMessage(gossipMessage); err != nil {
		return fmt.Errorf("message validation failed: %v", err)
	}

	// Process the verification request
	return r.ProcessVerificationRequest(gossipMessage)
}

// CheckSubscriptionStatus checks if the node is subscribed to consensus channel
func (r *Router) CheckSubscriptionStatus() (bool, error) {
	if r.verificationService == nil {
		return false, fmt.Errorf("verification service not initialized")
	}

	return r.IsSubscribedToConsensusChannel(), nil
}

// SendVerificationResponseWithValidation sends verification response with validation
func (r *Router) SendVerificationResponseWithValidation(accepted bool) error {
	if r.verificationService == nil {
		return fmt.Errorf("verification service not initialized")
	}

	// Check if we're subscribed before sending response
	if !r.IsSubscribedToConsensusChannel() {
		return fmt.Errorf("node not subscribed to consensus channel - cannot send verification response")
	}

	return r.SendVerificationResponse(accepted)
}
