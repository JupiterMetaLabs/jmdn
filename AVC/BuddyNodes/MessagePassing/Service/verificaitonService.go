package Service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
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
func (s *VerificationService) HandleVerifySubscription(logger_ctx context.Context, gossipMessage *PubSubMessages.GossipMessage) error {
	logger().Info(logger_ctx, "Handling verify subscription message",
		ion.String("sender", string(gossipMessage.Sender)),
		ion.String("function", "VerificationService.HandleVerifySubscription"))

	if s.buddyNode == nil {
		logger().Error(logger_ctx, "BuddyNode not available for verification",
			errors.New("buddy node not available for verification"),
			ion.String("function", "VerificationService.HandleVerifySubscription"))
		return fmt.Errorf("buddy node not available for verification")
	}

	// Check if we're subscribed to the consensus channel
	if !s.IsSubscribedToConsensusChannel() {
		logger().Info(logger_ctx, "Node not subscribed to consensus channel - sending ACK_FALSE",
			ion.String("function", "VerificationService.HandleVerifySubscription"))
		return s.SendVerificationResponse(logger_ctx, false)
	}

	// Node is ready and subscribed, respond with ACK_TRUE + PeerID
	hostID := s.buddyNode.Host.ID().String()
	logger().Info(logger_ctx, "Node is subscribed - sending ACK_TRUE with PeerID",
		ion.String("peer_id", hostID),
		ion.String("function", "VerificationService.HandleVerifySubscription"))

	return s.SendVerificationResponse(logger_ctx, true)
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
func (s *VerificationService) SendVerificationResponse(logger_ctx context.Context, accepted bool) error {
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
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	// Publish the verification response using the existing Publisher service
	err := Publisher.Publish(logger_ctx, s.buddyNode.PubSub, config.PubSub_ConsensusChannel, responseMessage, map[string]string{
		"verification_response": "true",
		"accepted":              fmt.Sprintf("%t", accepted),
		"peer_id":               s.buddyNode.PeerID.String(),
	})

	if err != nil {
		logger().Error(logger_ctx, "Failed to publish verification response", err,
			ion.String("accepted", fmt.Sprintf("%t", accepted)),
			ion.String("function", "VerificationService.sendVerificationResponse"))
		return fmt.Errorf("failed to publish verification response: %v", err)
	}

	logger().Info(logger_ctx, "Published verification response",
		ion.String("accepted", fmt.Sprintf("%t", accepted)),
		ion.String("peer_id", s.buddyNode.PeerID.String()),
		ion.String("function", "VerificationService.sendVerificationResponse"))

	return nil
}

// VerifySubscriptions verifies that all expected peers are subscribed to the consensus channel
// This function is inspired by the Communication.go VerifySubscriptions function
func (s *VerificationService) VerifySubscriptions(logger_ctx context.Context, expectedPeers []peer.ID, timeout time.Duration) (map[peer.ID]string, error) {
	if s.buddyNode == nil || s.buddyNode.PubSub == nil {
		return nil, fmt.Errorf("buddy node or pubsub not available for verification")
	}

	startTime := time.Now().UTC()

	logger().Info(logger_ctx, "Starting subscription verification for expected peers",
		ion.Int("expected_count", len(expectedPeers)),
		ion.String("function", "VerificationService.VerifySubscriptions"))

	// Create a channel to signal when all responses are received
	doneChan := make(chan struct{})
	verificationResponses := make(map[peer.ID]string)
	var mu sync.Mutex

	// Store the original handler if it exists, to restore later
	s.buddyNode.PubSub.Mutex.RLock()
	originalHandler, hadHandler := s.buddyNode.PubSub.Handlers[config.PubSub_ConsensusChannel]
	s.buddyNode.PubSub.Mutex.RUnlock()

	// Create a wrapper handler that calls both the original handler and collects responses
	handler := func(msg *PubSubMessages.GossipMessage) {
		// IMPORTANT: Check for verification responses FIRST before calling original handler
		// This ensures we capture verification ACKs before they're consumed by other handlers
		if msg.Data != nil && msg.Data.ACK != nil {
			// Check if this is a verification response with all required fields
			if msg.Data.ACK.Status == config.Type_ACK_True && msg.Data.ACK.Stage == config.Type_VerifySubscription {
				// Parse peer ID from the response
				peerID, err := peer.Decode(msg.Data.ACK.PeerID)
				if err != nil {
					logger().Error(logger_ctx, "Failed to decode peer ID from response", err,
						ion.String("function", "VerificationService.VerifySubscriptions"))
				} else {
					// Check if this peer is in our expected peers list
					isExpectedPeer := slices.Contains(expectedPeers, peerID)

					// Detailed Debug Logging for mismatched peers
					if !isExpectedPeer {
						var expectedStrs []string
						for _, p := range expectedPeers {
							expectedStrs = append(expectedStrs, p.String())
						}
						logger().Warn(logger_ctx, "Peer mismatch during verification",
							ion.String("received_peer", peerID.String()),
							ion.String("expected_peers", strings.Join(expectedStrs, ", ")),
							ion.String("function", "VerificationService.VerifySubscriptions"))
					}

					if isExpectedPeer {
						mu.Lock()
						// Only add if not already present (avoid duplicates)
						if _, exists := verificationResponses[peerID]; !exists {
							verificationResponses[peerID] = msg.Data.ACK.PeerID
							logger().Info(logger_ctx, "Received verification ACK from expected peer",
								ion.String("peer", peerID.String()),
								ion.Int("total_received", len(verificationResponses)),
								ion.Int("expected", len(expectedPeers)),
								ion.String("function", "VerificationService.VerifySubscriptions"))

							// Check if we have all responses and signal completion
							if len(verificationResponses) >= len(expectedPeers) {
								select {
								case doneChan <- struct{}{}:
									logger().Info(logger_ctx, "All verification responses received, signaling completion",
										ion.Int("received", len(verificationResponses)),
										ion.Int("expected", len(expectedPeers)),
										ion.String("function", "VerificationService.VerifySubscriptions"))
								default:
									// Channel already closed or signaled
								}
							}
						}
						mu.Unlock()
					} else {
						logger().Info(logger_ctx, "Received verification ACK from unexpected peer",
							ion.String("peer", peerID.String()),
							ion.String("function", "VerificationService.VerifySubscriptions"))
					}
				}
			}
		}

		// Call the original handler AFTER processing verification responses
		// This prevents the original handler from consuming verification messages before we see them
		if hadHandler && originalHandler != nil {
			originalHandler(msg)
		}
	}

	// Subscribe to the consensus channel with the wrapper handler
	if err := Connector.Subscribe(logger_ctx, s.buddyNode.PubSub, config.PubSub_ConsensusChannel, handler); err != nil {
		return nil, fmt.Errorf("failed to subscribe to consensus channel for verification: %v", err)
	}

	// Give the subscription a moment to fully establish and wait for peers to be connected on the topic
	// This prevents a race condition where the PubSub mesh isn't formed yet
	pubsubTopic, exists := s.buddyNode.PubSub.TopicsMap[config.PubSub_ConsensusChannel]
	if !exists {
		logger().Warn(logger_ctx, "Topic not found in TopicsMap, proceeding with fixed delay",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "VerificationService.VerifySubscriptions"))
		time.Sleep(200 * time.Millisecond)
	} else {
		// Wait for expected peers to appear in the topic mesh
		meshTimeout := time.After(2 * time.Second)
		meshTicker := time.NewTicker(50 * time.Millisecond)
		defer meshTicker.Stop()

		logger().Info(logger_ctx, "Waiting for PubSub mesh to form",
			ion.Int("expected_peers", len(expectedPeers)),
			ion.String("function", "VerificationService.VerifySubscriptions"))

	MeshWaitLoop:
		for {
			select {
			case <-meshTimeout:
				logger().Warn(logger_ctx, "Timeout waiting for PubSub mesh to form, proceeding anyway",
					ion.String("function", "VerificationService.VerifySubscriptions"))
				break MeshWaitLoop
			case <-meshTicker.C:
				peers := pubsubTopic.ListPeers()
				connectedCount := 0

				for _, p := range peers {
					if slices.Contains(expectedPeers, p) {
						connectedCount++
					}
				}

				if connectedCount >= len(expectedPeers) {
					logger().Info(logger_ctx, "PubSub mesh formed with all expected peers",
						ion.Int("connected", connectedCount),
						ion.Int("total_peers_in_topic", len(peers)),
						ion.String("function", "VerificationService.VerifySubscriptions"))
					break MeshWaitLoop
				} else {
					// Log debug info occasionally (every 10th tick approx, or just use a counter)
					// For now, let's log every tick if we are close to timeout or just debug
					// But to avoid noise, let's just log if count > 0 but < expected
					if connectedCount > 0 {
						logger().Debug(logger_ctx, "Waiting for full mesh",
							ion.Int("connected", connectedCount),
							ion.Int("expected", len(expectedPeers)),
							ion.Int("total_peers_in_topic", len(peers)),
							ion.String("function", "VerificationService.VerifySubscriptions"))
					}
				}
			}
		}
	}

	logger().Info(logger_ctx, "Proceeding with verification request",
		ion.String("function", "VerificationService.VerifySubscriptions"))

	// Send verification request to all expected peers
	verificationMessage := "Please verify your subscription to the consensus channel"
	ackMessage := PubSubMessages.NewACKBuilder().True_ACK_Message(s.buddyNode.PeerID, config.Type_VerifySubscription)

	message := PubSubMessages.NewMessageBuilder(nil).
		SetMessage(verificationMessage).
		SetSender(s.buddyNode.PeerID).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	if err := Publisher.Publish(logger_ctx, s.buddyNode.PubSub, config.PubSub_ConsensusChannel, message, map[string]string{
		"verification_request": "true",
		"requester":            s.buddyNode.PeerID.String(),
	}); err != nil {
		// Unsubscribe before returning error
		Connector.Unsubscribe(s.buddyNode.PubSub, config.PubSub_ConsensusChannel)
		close(doneChan)
		return nil, fmt.Errorf("failed to publish verification request: %v", err)
	}

	logger().Info(logger_ctx, "Published verification request to consensus channel",
		ion.String("function", "VerificationService.VerifySubscriptions"))

	// Wait for either all responses or timeout using select
	// Wait for either all responses or timeout using select
	// Calculate remaining time from the original timeout
	elapsed := time.Since(startTime)
	remaining := timeout - elapsed
	if remaining < 0 {
		remaining = 0
	}

	select {
	case <-doneChan:
		duration := time.Since(startTime)
		logger().Info(logger_ctx, "Received all expected verification responses",
			ion.Int("received", len(verificationResponses)),
			ion.Int("expected", len(expectedPeers)),
			ion.Float64("duration_ms", float64(duration.Milliseconds())),
			ion.String("function", "VerificationService.VerifySubscriptions"))
	case <-time.After(remaining):
		mu.Lock()
		finalCount := len(verificationResponses)
		mu.Unlock()
		duration := time.Since(startTime)
		logger().Warn(logger_ctx, "Verification timeout reached",
			ion.Int("received", finalCount),
			ion.Int("expected", len(expectedPeers)),
			ion.Float64("duration_ms", float64(duration.Milliseconds())),
			ion.String("function", "VerificationService.VerifySubscriptions"))
	}

	// Final count
	mu.Lock()
	finalCount := len(verificationResponses)
	mu.Unlock()

	logger().Info(logger_ctx, "Subscription verification completed",
		ion.Int("verified", finalCount),
		ion.Int("expected", len(expectedPeers)),
		ion.String("function", "VerificationService.VerifySubscriptions"))

	// Restore the original handler
	s.buddyNode.PubSub.Mutex.Lock()
	if hadHandler {
		s.buddyNode.PubSub.Handlers[config.PubSub_ConsensusChannel] = originalHandler
	} else {
		delete(s.buddyNode.PubSub.Handlers, config.PubSub_ConsensusChannel)
	}
	s.buddyNode.PubSub.Mutex.Unlock()

	// Unsubscribe from the channel
	if err := Connector.Unsubscribe(s.buddyNode.PubSub, config.PubSub_ConsensusChannel); err != nil {
		logger().Error(logger_ctx, "Failed to unsubscribe from consensus channel", err,
			ion.String("function", "VerificationService.VerifySubscriptions"))
	}

	// Close the done channel to prevent any late responses from blocking
	close(doneChan)

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
