package Sequencer

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Sequencer/Router"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ResponseHandler manages ACK responses from peers
type ResponseHandler struct {
	responses map[peer.ID]chan bool
	peerIDs   map[peer.ID]string // Store the actual PeerID from ACK responses
	roles     map[peer.ID]string // Store the role (main/backup) for each peer
	mutex     sync.RWMutex
}

// NewResponseHandler creates a new response handler
func NewResponseHandler() *ResponseHandler {
	return &ResponseHandler{
		responses: make(map[peer.ID]chan bool),
		peerIDs:   make(map[peer.ID]string),
		roles:     make(map[peer.ID]string),
	}
}

// RegisterPeer registers a peer for response tracking with role
func (rh *ResponseHandler) RegisterPeer(peerID peer.ID, role string) chan bool {
	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	responseChan := make(chan bool, 1)
	rh.responses[peerID] = responseChan
	rh.roles[peerID] = role
	fmt.Printf("=== ResponseHandler.RegisterPeer: Created channel %p for peer: %s (role: %s) ===\n", responseChan, peerID, role)
	return responseChan
}

// HandleResponse handles an ACK response from a peer
func (rh *ResponseHandler) HandleResponse(peerID peer.ID, accepted bool, role string) {
	fmt.Printf("=== ResponseHandler.HandleResponse called for peer: %s (accepted: %t) ===\n", peerID, accepted)

	// Debug: Show all registered peers
	rh.mutex.RLock()
	fmt.Printf("=== ResponseHandler DEBUG: All registered peers (roles): ===\n")
	for p, r := range rh.roles {
		fmt.Printf("  - Peer: %s, Role: %s\n", p, r)
	}
	fmt.Printf("=== ResponseHandler DEBUG: All registered peers (channels): ===\n")
	for p, ch := range rh.responses {
		fmt.Printf("  - Peer: %s, Channel: %p\n", p, ch)
	}
	fmt.Printf("=== End of registered peers ===\n")

	storedRole, exists := rh.roles[peerID]
	rh.mutex.RUnlock()

	if !exists {
		storedRole = "unknown" // fallback
	}

	fmt.Printf("=== ResponseHandler: Using stored role '%s' for peer: %s ===\n", storedRole, peerID)

	// Track accepted peers in the global tracker
	if accepted {
		tracker := PubSubMessages.GetSubscriptionTracker()
		tracker.MarkPeerAccepted(peerID, storedRole)
		fmt.Printf("=== Global tracker: Marked peer %s as accepted (role: %s, total: %d) ===\n", peerID, storedRole, tracker.GetActiveCount())
	}

	rh.mutex.RLock()
	responseChan, exists := rh.responses[peerID]
	rh.mutex.RUnlock()

	fmt.Printf("ResponseHandler: Channel exists for peer %s: %t\n", peerID, exists)
	if exists {
		fmt.Printf("ResponseHandler: Attempting to send to channel for peer %s\n", peerID)
		select {
		case responseChan <- accepted:
			fmt.Printf("ResponseHandler: Successfully sent response to channel for peer %s\n", peerID)
		default:
			fmt.Printf("ResponseHandler: Channel full or closed for peer %s\n", peerID)
			// Channel is full or closed, ignore
		}
	} else {
		fmt.Printf("ResponseHandler: No channel found for peer %s\n", peerID)
		fmt.Printf("ResponseHandler: Available channels: %v\n", rh.responses)
	}
}

// HandleResponseWithPeerID handles an ACK response from a peer with PeerID information
func (rh *ResponseHandler) HandleResponseWithPeerID(peerID peer.ID, accepted bool, responsePeerID string) {
	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	// Store the PeerID from the response
	if accepted && responsePeerID != "" {
		rh.peerIDs[peerID] = responsePeerID
	}

	// Handle the response
	if responseChan, exists := rh.responses[peerID]; exists {
		select {
		case responseChan <- accepted:
		default:
			// Channel is full or closed, ignore
		}
	}
}

// GetVerifiedPeerIDs returns the map of verified PeerIDs
func (rh *ResponseHandler) GetVerifiedPeerIDs() map[peer.ID]string {
	rh.mutex.RLock()
	defer rh.mutex.RUnlock()

	// Create a copy to avoid race conditions
	result := make(map[peer.ID]string)
	for k, v := range rh.peerIDs {
		result[k] = v
	}
	return result
}

// UnregisterPeer removes a peer from response tracking
func (rh *ResponseHandler) UnregisterPeer(peerID peer.ID) {
	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	if responseChan, exists := rh.responses[peerID]; exists {
		close(responseChan)
		delete(rh.responses, peerID)
	}

	// Also clean up the peerIDs and roles maps
	delete(rh.peerIDs, peerID)
	delete(rh.roles, peerID)
}

// cleanupResponseHandler removes all peers from the response handler
func cleanupResponseHandler(rh *ResponseHandler) {
	if rh == nil {
		return
	}

	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	// Close all channels
	for _, ch := range rh.responses {
		close(ch)
	}

	// Clear all maps
	rh.responses = make(map[peer.ID]chan bool)
	rh.peerIDs = make(map[peer.ID]string)
	rh.roles = make(map[peer.ID]string)
}

// AskForSubscription asks peers for subscription with backup node fallback
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func AskForSubscription(Listener *MessagePassing.StructListener, topic string, consensus *Consensus) error {
	// Reset the global tracker at the start
	tracker := PubSubMessages.GetSubscriptionTracker()
	tracker.Reset()

	responseHandler := NewResponseHandler()

	// First, try main peers (up to 13)
	mainAccepted, mainTotal := askPeersForSubscription(Listener, topic, consensus.PeerList.MainPeers, responseHandler, "main")
	mainFailed := mainTotal - mainAccepted

	log.Printf("Main peers results: %d accepted, %d failed out of %d", mainAccepted, mainFailed, mainTotal)

	// If we have exactly 13 main peers, we're done
	if mainAccepted == config.MaxMainPeers {
		log.Printf("Perfect! Got exactly %d main peers for consensus (1 creator + 13 subscribers = 14 total)", config.MaxMainPeers)
		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())
			cleanupResponseHandler(responseHandler)
			return nil
		}
	}

	// Check if too many main nodes failed
	// If we have backup peers, we need fewer main peers to accept (allows for replacement)
	// If no backup peers, we need ALL main peers to be accepted
	var minMainRequired int
	if config.MaxBackupPeers > 0 {
		minMainRequired = config.MaxMainPeers - config.MaxBackupPeers
	} else {
		// No backup peers available - require all main peers
		minMainRequired = config.MaxMainPeers
	}

	if mainAccepted < minMainRequired {
		cleanupResponseHandler(responseHandler)
		return fmt.Errorf("too many main nodes failed: got %d, need at least %d (configured with %d main peers and %d backup peers)", mainAccepted, minMainRequired, config.MaxMainPeers, config.MaxBackupPeers)
	}

	// If we have less than required main peers, use backup peers as replacements
	if mainAccepted < config.MaxMainPeers && config.MaxBackupPeers > 0 {
		needed := config.MaxMainPeers - mainAccepted
		log.Printf("Need %d backup nodes as replacements for failed main nodes", needed)

		// Check if we have backup peers available
		if len(consensus.PeerList.BackupPeers) == 0 {
			log.Printf("No backup peers available to replace failed main nodes")
		}

		// Limit backup peers to only what we need
		backupPeersToTry := consensus.PeerList.BackupPeers
		if len(backupPeersToTry) > needed {
			backupPeersToTry = backupPeersToTry[:needed]
		}

		backupAccepted, backupTotal := askPeersForSubscription(Listener, topic, backupPeersToTry, responseHandler, "backup")

		log.Printf("Backup peers results: %d accepted out of %d tried", backupAccepted, backupTotal)

		totalAccepted := mainAccepted + backupAccepted

		log.Printf("Final subscription results: %d main + %d backup = %d total subscribers", mainAccepted, backupAccepted, totalAccepted)

		// Ensure we have exactly 13 subscribers (1 creator + 13 subscribers = 14 total)
		if totalAccepted != config.MaxMainPeers {
			return fmt.Errorf("insufficient subscribers for consensus: got %d, need exactly %d (1 creator + 13 subscribers = 14 total)", totalAccepted, config.MaxMainPeers)
		}

		log.Printf("Successfully achieved consensus: 1 creator + %d subscribers = 14 total nodes", totalAccepted)

		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())

			// Cleanup response handler channels now that we're done
			cleanupResponseHandler(responseHandler)

			return nil
		}
	}

	// Check if we got exactly the required number of main peers (no backups needed)
	if mainAccepted == config.MaxMainPeers {
		log.Printf("Successfully achieved consensus with %d main peers (no backups needed)", mainAccepted)

		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())
			cleanupResponseHandler(responseHandler)
			return nil
		}
	}

	// Cleanup response handler before returning error
	cleanupResponseHandler(responseHandler)

	return fmt.Errorf("global tracker validation failed: got %d, need %d", tracker.GetActiveCount(), config.MaxMainPeers)
}

// VerifySubscriptions publishes a verification message to the pubsub channel and collects ACK responses
// All subscribed nodes should reply with ACK_TRUE status and their PeerID
// This function now uses the Router for centralized verification handling
func VerifySubscriptions(gps *PubSubMessages.GossipPubSub, consensus *Consensus) (map[peer.ID]string, error) {
	log.Printf("Starting pubsub-based subscription verification for %d main peers", len(consensus.PeerList.MainPeers))

	// Create Router instance for centralized verification handling
	router := createRouter(gps)
	defer router.Close()

	// Use the Router to verify subscriptions with a 10-second timeout
	verificationResponses, err := router.VerifySubscriptions(consensus.PeerList.MainPeers, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("router verification failed: %v", err)
	}

	log.Printf("Subscription verification completed: %d peers verified out of %d expected",
		len(verificationResponses), len(consensus.PeerList.MainPeers))

	return verificationResponses, nil
}

// createRouter creates a new Router instance for the given GossipPubSub
func createRouter(gps *PubSubMessages.GossipPubSub) *Router.Router {
	// Create a BuddyNode from the GossipPubSub to use with Router
	buddyNode := &PubSubMessages.BuddyNode{
		PeerID: gps.Host.ID(),
		Host:   gps.Host,
		PubSub: gps,
	}

	// Create Router instance for centralized verification handling
	return Router.NewRouter(buddyNode)
}

// ProcessVerificationMessage processes incoming verification messages using the Router
func ProcessVerificationMessage(gps *PubSubMessages.GossipPubSub, gossipMessage *PubSubMessages.GossipMessage) error {
	// Create Router instance for centralized verification handling
	router := createRouter(gps)
	defer router.Close()
	// Use the Router to process the verification message
	return router.ProcessCompleteVerification(gossipMessage)
}

// CheckNodeSubscriptionStatus checks if a node is subscribed using the Router
func CheckNodeSubscriptionStatus(gps *PubSubMessages.GossipPubSub) (bool, error) {
	// Create Router instance for centralized verification handling
	router := createRouter(gps)
	defer router.Close()
	// Use the Router to check subscription status
	return router.CheckSubscriptionStatus()
}

// SendVerificationResponseWithRouter sends verification response using the Router
func SendVerificationResponseWithRouter(gps *PubSubMessages.GossipPubSub, accepted bool) error {
	// Create Router instance for centralized verification handling
	router := createRouter(gps)
	defer router.Close()
	// Use the Router to send verification response with validation
	return router.SendVerificationResponseWithValidation(accepted)
}

// GetVerificationStatsWithRouter gets verification statistics using the Router
func GetVerificationStatsWithRouter(gps *PubSubMessages.GossipPubSub) map[string]interface{} {
	// Create Router instance for centralized verification handling
	router := createRouter(gps)

	// Use the Router to get verification stats
	return router.GetVerificationStats()
}

// askPeersForSubscription asks a list of peers for subscription
func askPeersForSubscription(Listener *MessagePassing.StructListener, topic string, peerAddrs []peer.ID, responseHandler *ResponseHandler, peerType string) (int, int) {
	fmt.Println("askPeersForSubscription", peerAddrs)
	if len(peerAddrs) == 0 {
		log.Printf("No %s peers to ask for subscription", peerType)
		return 0, 0
	}

	// Get initial tracker count before this call
	tracker := PubSubMessages.GetSubscriptionTracker()
	initialCount := tracker.GetActiveCount()

	accepted := make(map[string]bool)
	var wg sync.WaitGroup
	var mu sync.Mutex

	log.Printf("Asking %d %s peers for subscription to topic: %s", len(peerAddrs), peerType, topic)
	log.Printf("Initial tracker count: %d", initialCount)

	// Get host from GossipPubSub (not create BuddyNode yet)
	host := Listener.ListenerBuddyNode.Host

	// Response handler is now set up in the buddy nodes themselves
	// No need to set up additional handlers here

	for _, peerID := range peerAddrs {
		// Register peer for response tracking
		responseChan := responseHandler.RegisterPeer(peerID, peerType)

		wg.Add(1)
		go func(peerID peer.ID) {
			defer wg.Done()
			// DON'T unregister here - keep the role stored for HandleResponse
			// defer responseHandler.UnregisterPeer(peerID)

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Create proper message with ACK for subscription request
			ackMessage := PubSubMessages.NewACKBuilder().True_ACK_Message(host.ID(), config.Type_AskForSubscription)
			message := PubSubMessages.NewMessageBuilder(nil).
				SetSender(host.ID()).
				SetMessage(fmt.Sprintf("Requesting subscription to channel: %s", topic)).
				SetTimestamp(time.Now().UTC().Unix()).
				SetACK(ackMessage)

			// Marshal the message to JSON
			messageBytes, err := json.Marshal(message)
			if err != nil {
				log.Printf("Failed to marshal subscription request message: %v", err)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
				return
			}

			// Send subscription request via SubmitMessageProtocol using existing function
			if err := Listener.SendMessageToPeer(peerID, string(messageBytes)); err != nil {
				log.Printf("Failed to send subscription request to %s %s: %v", peerType, peerID, err)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
				return
			}

			log.Printf("Sent subscription request to %s peer: %s, waiting for ACK...", peerType, peerID)
			fmt.Printf("=== askPeersForSubscription: Waiting for response from peer: %s ===\n", peerID)
			fmt.Printf("=== askPeersForSubscription: Response channel: %p for peer: %s ===\n", responseChan, peerID)

			// Wait for response with timeout
			select {
			case response := <-responseChan:
				fmt.Printf("=== askPeersForSubscription: Received response from peer: %s (accepted: %t) ===\n", peerID, response)
				mu.Lock()
				accepted[peerID.String()] = response
				mu.Unlock()

				if response {
					log.Printf("%s peer %s accepted subscription", peerType, peerID)
				} else {
					log.Printf("%s peer %s rejected subscription", peerType, peerID)
				}
			case <-ctx.Done():
				fmt.Printf("=== askPeersForSubscription: TIMEOUT waiting for response from peer: %s ===\n", peerID)
				fmt.Printf("=== askPeersForSubscription: Timeout context done for peer: %s ===\n", peerID)
				log.Printf("Timeout waiting for ACK from %s peer: %s", peerType, peerID)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
			}
		}(peerID)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	log.Printf("All goroutines completed for %s peers", peerType)

	// Give some time for late-arriving responses to be processed
	time.Sleep(500 * time.Millisecond)

	// Calculate how many NEW peers were accepted in this call
	finalCount := tracker.GetActiveCount()
	newAccepted := finalCount - initialCount

	log.Printf("Tracker count: %d -> %d (new: %d) for %s peers", initialCount, finalCount, newAccepted, peerType)

	// Note: Don't cleanup channels here - they're still being used
	// Cleanup will happen when the next batch starts or when consensus completes
	return newAccepted, len(peerAddrs)
}

// ValidateConsensusConfiguration validates that the consensus configuration is correct
// NEW Architecture: 1 creator + MaxMainPeers final buddy nodes (no separate backup peers)
// Final buddy nodes are selected by trying main candidates first, then filling from backup candidates
// After selection, only the final connected peers are kept (no backup peers stored separately)
func ValidateConsensusConfiguration(consensus *Consensus) error {
	// Check main peers count (should be exactly MaxMainPeers - these are the final connected buddy nodes)
	if len(consensus.PeerList.MainPeers) != config.MaxMainPeers {
		return fmt.Errorf("main peers count must be exactly %d (final buddy nodes), got %d", config.MaxMainPeers, len(consensus.PeerList.MainPeers))
	}

	// Backup peers are now optional/empty - they're only used during selection, not stored separately
	// Allow empty backup peers since final buddy nodes are all in MainPeers after selection
	if len(consensus.PeerList.BackupPeers) > 0 && len(consensus.PeerList.BackupPeers) != config.MaxBackupPeers {
		return fmt.Errorf("if backup peers are set, count must be exactly %d, got %d", config.MaxBackupPeers, len(consensus.PeerList.BackupPeers))
	}

	// Check for duplicate peer IDs within main peers
	if err := checkForDuplicatePeerIDs(consensus.PeerList.MainPeers); err != nil {
		return fmt.Errorf("duplicate peer ID found in main peers: %w", err)
	}

	// Check for duplicate peer IDs within backup peers (if any)
	if len(consensus.PeerList.BackupPeers) > 0 {
		if err := checkForDuplicatePeerIDs(consensus.PeerList.BackupPeers); err != nil {
			return fmt.Errorf("duplicate peer ID found in backup peers: %w", err)
		}
	}

	// Check for duplicate peer IDs between main and backup peers
	allPeers := make(map[peer.ID]bool)

	// Add main peers to the map
	for _, peerID := range consensus.PeerList.MainPeers {
		allPeers[peerID] = true
	}

	// Check backup peers against main peers (if any)
	for _, peerID := range consensus.PeerList.BackupPeers {
		if allPeers[peerID] {
			return fmt.Errorf("duplicate peer ID found between main and backup peers: %s", peerID)
		}
		allPeers[peerID] = true
	}

	// Total peers validation: only check main peers (backup peers may be empty)
	totalPeers := len(consensus.PeerList.MainPeers)
	if totalPeers != config.MaxMainPeers {
		return fmt.Errorf("total main peers count must be exactly %d (final buddy nodes), got %d", config.MaxMainPeers, totalPeers)
	}

	log.Printf("Consensus configuration validated: %d final buddy nodes (main peers), %d backup peers (1 creator + %d active = %d total allowed)",
		len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.MainPeers)+1)

	return nil
}

// checkForDuplicatePeerIDs checks for duplicate peer IDs within a single peer list
func checkForDuplicatePeerIDs(peerList []peer.ID) error {
	peerMap := make(map[peer.ID]bool)

	for _, peerID := range peerList {
		if peerMap[peerID] {
			return fmt.Errorf("duplicate peer ID found: %s", peerID)
		}
		peerMap[peerID] = true
	}

	return nil
}
