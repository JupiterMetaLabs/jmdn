package Service

import (
	"slices"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// VerificationService handles subscription verification operations
type VerificationService struct {
	buddyNode *PubSubMessages.BuddyNode
}

// NewVerificationService creates a new verification service
func NewVerificationService(buddyNode *PubSubMessages.BuddyNode) *VerificationService {
	return &VerificationService{
		buddyNode: buddyNode,
	}
}

// HandleVerifySubscription handles incoming subscription verification requests
func (s *VerificationService) HandleVerifySubscription(gossipMessage *PubSubMessages.GossipMessage) error {
	log.LogConsensusInfo("Handling verify subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("sender", string(gossipMessage.Sender)),
		zap.String("function", "VerificationService.HandleVerifySubscription"))

	if s.buddyNode == nil {
		log.LogConsensusError("BuddyNode not available for verification",
			nil,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "VerificationService.HandleVerifySubscription"))
		return fmt.Errorf("buddy node not available for verification")
	}

	// Check if we're subscribed to the consensus channel
	if !s.IsSubscribedToConsensusChannel() {
		log.LogConsensusInfo("Node not subscribed to consensus channel - sending ACK_FALSE",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "VerificationService.HandleVerifySubscription"))
		return s.SendVerificationResponse(false)
	}

	// Node is ready and subscribed, respond with ACK_TRUE + PeerID
	hostID := s.buddyNode.Host.ID().String()
	log.LogConsensusInfo(fmt.Sprintf("Node is subscribed - sending ACK_TRUE with PeerID %s", hostID),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("peer_id", hostID),
		zap.String("function", "VerificationService.HandleVerifySubscription"))

	return s.SendVerificationResponse(true)
}

// isSubscribedToConsensusChannel checks if the node is subscribed to the consensus channel
func (s *VerificationService) IsSubscribedToConsensusChannel() bool {
	if s.buddyNode == nil || s.buddyNode.PubSub == nil {
		return false
	}

	s.buddyNode.PubSub.Mutex.RLock()
	defer s.buddyNode.PubSub.Mutex.RUnlock()

	// Check if we're subscribed to the consensus channel
	return s.buddyNode.PubSub.Topics[config.PubSub_ConsensusChannel]
}

// sendVerificationResponse sends a verification response back to the requesting node
func (s *VerificationService) SendVerificationResponse(accepted bool) error {
	if s.buddyNode == nil || s.buddyNode.PubSub == nil {
		return fmt.Errorf("buddy node or pubsub not available for sending response")
	}

	// Create ACK message
	var ackMessage *PubSubMessages.ACK
	if accepted {
		ackMessage = PubSubMessages.NewACKBuilder().True_ACK_Message(s.buddyNode.PeerID, config.Type_VerifySubscription)
	} else {
		ackMessage = PubSubMessages.NewACKBuilder().False_ACK_Message(s.buddyNode.PeerID, config.Type_VerifySubscription)
	}

	// Create verification response message
	responseMessage := PubSubMessages.NewMessageBuilder(nil).
		SetSender(s.buddyNode.PeerID).
		SetMessage(fmt.Sprintf("Verification response: %t", accepted)).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackMessage)

	// Publish the verification response using the existing Publisher service
	err := Publisher.Publish(s.buddyNode.PubSub, config.PubSub_ConsensusChannel, responseMessage, map[string]string{
		"verification_response": "true",
		"accepted":              fmt.Sprintf("%t", accepted),
		"peer_id":               s.buddyNode.PeerID.String(),
	})

	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to publish verification response: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("accepted", fmt.Sprintf("%t", accepted)),
			zap.String("function", "VerificationService.sendVerificationResponse"))
		return fmt.Errorf("failed to publish verification response: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Published verification response: accepted=%t", accepted),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("accepted", fmt.Sprintf("%t", accepted)),
		zap.String("peer_id", s.buddyNode.PeerID.String()),
		zap.String("function", "VerificationService.sendVerificationResponse"))

	return nil
}

// VerifySubscriptions verifies that all expected peers are subscribed to the consensus channel
// This function is inspired by the Communication.go VerifySubscriptions function
func (s *VerificationService) VerifySubscriptions(expectedPeers []peer.ID, timeout time.Duration) (map[peer.ID]string, error) {
	if s.buddyNode == nil || s.buddyNode.PubSub == nil {
		return nil, fmt.Errorf("buddy node or pubsub not available for verification")
	}

	log.LogConsensusInfo(fmt.Sprintf("Starting subscription verification for %d expected peers", len(expectedPeers)),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("expected_count", fmt.Sprintf("%d", len(expectedPeers))),
		zap.String("function", "VerificationService.VerifySubscriptions"))

	// Create a channel to collect verification responses
	verificationResponses := make(map[peer.ID]string)
	var mu sync.Mutex
	responseCount := 0

	// Subscribe to the consensus channel to receive verification responses
	handler := func(msg *PubSubMessages.GossipMessage) {
		// Check if message has ACK data
		if msg.Data != nil && msg.Data.ACK != nil {
			// Check if this is a verification response with all required fields
			if msg.Data.ACK.Status == config.Type_ACK_True && msg.Data.ACK.Stage == config.Type_VerifySubscription {
				// Parse peer ID from the response
				peerID, err := peer.Decode(msg.Data.ACK.PeerID)
				if err != nil {
					log.LogConsensusError(fmt.Sprintf("Failed to decode peer ID from response: %v", err), err,
						zap.String("topic", log.Consensus_TOPIC),
						zap.String("function", "VerificationService.VerifySubscriptions"))
					return
				}

				// Check if this peer is in our expected peers list
				isExpectedPeer := slices.Contains(expectedPeers, peerID)

				if isExpectedPeer {
					mu.Lock()
					verificationResponses[peerID] = msg.Data.ACK.PeerID
					responseCount++
					log.LogConsensusInfo(fmt.Sprintf("Received verification ACK from expected peer: %s", peerID),
						zap.String("topic", log.Consensus_TOPIC),
						zap.String("peer", peerID.String()),
						zap.String("function", "VerificationService.VerifySubscriptions"))
					mu.Unlock()
				} else {
					log.LogConsensusInfo(fmt.Sprintf("Received verification ACK from unexpected peer: %s (ignoring)", peerID),
						zap.String("topic", log.Consensus_TOPIC),
						zap.String("peer", peerID.String()),
						zap.String("function", "VerificationService.VerifySubscriptions"))
				}
			}
		}
	}

	// Subscribe to the consensus channel
	if err := Connector.Subscribe(s.buddyNode.PubSub, config.PubSub_ConsensusChannel, handler); err != nil {
		return nil, fmt.Errorf("failed to subscribe to consensus channel for verification: %v", err)
	}

	// Send verification request to all expected peers
	verificationMessage := "Please verify your subscription to the consensus channel"
	ackMessage := PubSubMessages.NewACKBuilder().True_ACK_Message(s.buddyNode.PeerID, config.Type_VerifySubscription)

	message := PubSubMessages.NewMessageBuilder(nil).
		SetMessage(verificationMessage).
		SetSender(s.buddyNode.PeerID).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackMessage)

	if err := Publisher.Publish(s.buddyNode.PubSub, config.PubSub_ConsensusChannel, message, map[string]string{
		"verification_request": "true",
		"requester":            s.buddyNode.PeerID.String(),
	}); err != nil {
		// Unsubscribe before returning error
		Connector.Unsubscribe(s.buddyNode.PubSub, config.PubSub_ConsensusChannel)
		return nil, fmt.Errorf("failed to publish verification request: %v", err)
	}

	log.LogConsensusInfo("Published verification request to consensus channel",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "VerificationService.VerifySubscriptions"))

	// Wait for responses with timeout
	startTime := time.Now()
	for time.Since(startTime) < timeout {
		mu.Lock()
		currentCount := responseCount
		mu.Unlock()

		if currentCount >= len(expectedPeers) {
			log.LogConsensusInfo(fmt.Sprintf("Received all expected verification responses: %d/%d", currentCount, len(expectedPeers)),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("received", fmt.Sprintf("%d", currentCount)),
				zap.String("expected", fmt.Sprintf("%d", len(expectedPeers))),
				zap.String("function", "VerificationService.VerifySubscriptions"))
			break
		}

		// Check every 100ms
		time.Sleep(100 * time.Millisecond)
	}

	// Final count
	mu.Lock()
	finalCount := responseCount
	mu.Unlock()

	log.LogConsensusInfo(fmt.Sprintf("Subscription verification completed: %d peers verified out of %d expected", finalCount, len(expectedPeers)),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("verified", fmt.Sprintf("%d", finalCount)),
		zap.String("expected", fmt.Sprintf("%d", len(expectedPeers))),
		zap.String("function", "VerificationService.VerifySubscriptions"))

	// Unsubscribe from the channel
	if err := Connector.Unsubscribe(s.buddyNode.PubSub, config.PubSub_ConsensusChannel); err != nil {
		log.LogConsensusError(fmt.Sprintf("Warning: failed to unsubscribe from consensus channel: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "VerificationService.VerifySubscriptions"))
	}

	return verificationResponses, nil
}

// GetVerificationStats returns statistics about the verification process
func (s *VerificationService) GetVerificationStats() map[string]interface{} {
	if s.buddyNode == nil {
		return map[string]interface{}{
			"error": "buddy node not available",
		}
	}

	s.buddyNode.PubSub.Mutex.RLock()
	defer s.buddyNode.PubSub.Mutex.RUnlock()

	return map[string]interface{}{
		"subscribed_topics":    len(s.buddyNode.PubSub.Topics),
		"connected_peers":      len(s.buddyNode.PubSub.Peers),
		"consensus_subscribed": s.buddyNode.PubSub.Topics[config.PubSub_ConsensusChannel],
		"peer_id":              s.buddyNode.PeerID.String(),
		"host_id":              s.buddyNode.Host.ID().String(),
	}
}
