package Sequencer

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Sequencer/Router"
	"gossipnode/Sequencer/common"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	// subscriptionAckTimeout is the maximum time we wait for a peer's subscription ACK.
	// Kept as a var so tests can shorten it.
	subscriptionAckTimeout = 30 * time.Second

	// subscriptionLateResponseBuffer allows late-arriving responses to be processed.
	// Kept as a var so tests can reduce test runtime.
	subscriptionLateResponseBuffer = 2 * time.Second
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
// Ensures: 1 creator + MaxMainPeers subscribers = MaxMainPeers + 1 total nodes
func AskForSubscription(Listener *MessagePassing.StructListener, topic string, consensus *Consensus) error {
	// Reset the global tracker at the start
	tracker := PubSubMessages.GetSubscriptionTracker()
	tracker.Reset()

	// IMPORTANT:
	// Use the consensus-level response handler that the Listener is wired to.
	// Creating a new handler here would mean ACKs get delivered to a different handler,
	// and the waits below will time out even if peers responded.
	responseHandler := consensus.ResponseHandler
	if responseHandler == nil {
		responseHandler = NewResponseHandler()
		consensus.ResponseHandler = responseHandler
	}

	// get count of main peers and backup peers from consensus peerlist
	mainPeersCount := len(consensus.PeerList.MainPeers)
	backupPeersCount := len(consensus.PeerList.BackupPeers)

	if mainPeersCount+backupPeersCount < config.MaxMainPeers {
		return fmt.Errorf("not enough peers to achieve consensus: got %d main + %d backup = %d total, need at least %d (MaxMainPeers)", mainPeersCount, backupPeersCount, mainPeersCount+backupPeersCount, config.MaxMainPeers)
	}

	// First, try main peers (MaxMainPeers)
	mainAcceptedCount, mainTotalCount, mainAcceptedPeers := askPeersForSubscription(
		Listener,
		topic,
		consensus.PeerList.MainPeers,
		responseHandler,
		"main",
	)
	mainFailed := mainTotalCount - mainAcceptedCount

	log.Printf("Main peers results: %d accepted, %d failed out of %d", mainAcceptedCount, mainFailed, mainTotalCount)

	// If we have exactly MaxMainPeers main peers, we're done
	if mainAcceptedCount == config.MaxMainPeers {
		log.Printf("Perfect! Got exactly %d MaxMainPeers for consensus (1 creator + MaxMainPeers subscribers)", config.MaxMainPeers)
		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())
			setFinalConsensusPeers(consensus, mainAcceptedPeers)
			cleanupResponseHandler(responseHandler)
			return nil
		}
	}

	// If we have less than required main peers, use backup peers as replacements
	if mainAcceptedCount < config.MaxMainPeers && config.MaxBackupPeers > 0 {
		needed := config.MaxMainPeers - mainAcceptedCount
		log.Printf("Need %d backup nodes as replacements for failed main nodes", needed)

		// Check if we have backup peers available
		if len(consensus.PeerList.BackupPeers) == 0 {
			log.Printf("No backup peers available to replace failed main nodes")
		}

		// Retry loop: keep trying backup peers until we have enough or run out
		backupPeersIndex := 0
		backupAcceptedCount := 0
		backupAcceptedPeers := make([]peer.ID, 0, needed)
		stillNeeded := needed
		allBackupPeers := consensus.PeerList.BackupPeers

		for stillNeeded > 0 && backupPeersIndex < len(allBackupPeers) {
			// Calculate how many backup peers to try in this iteration
			// Try at least 'stillNeeded' peers, but don't exceed available peers
			peersToTryCount := stillNeeded
			if backupPeersIndex+peersToTryCount > len(allBackupPeers) {
				peersToTryCount = len(allBackupPeers) - backupPeersIndex
			}

			// Get the next batch of backup peers to try
			backupPeersToTry := allBackupPeers[backupPeersIndex : backupPeersIndex+peersToTryCount]
			log.Printf("Trying %d backup peers (index %d to %d), still need %d more",
				peersToTryCount, backupPeersIndex, backupPeersIndex+peersToTryCount-1, stillNeeded)

			// Ask this batch for subscription
			batchAcceptedCount, batchTotalCount, batchAcceptedPeers := askPeersForSubscription(
				Listener,
				topic,
				backupPeersToTry,
				responseHandler,
				"backup",
			)
			log.Printf("Backup batch results: %d accepted out of %d tried", batchAcceptedCount, batchTotalCount)

			// Update counters
			backupAcceptedCount += batchAcceptedCount
			backupAcceptedPeers = append(backupAcceptedPeers, batchAcceptedPeers...)
			backupPeersIndex += peersToTryCount
			stillNeeded -= batchAcceptedCount

			// If we got some acceptances, log progress
			if batchAcceptedCount > 0 {
				log.Printf(
					"Progress: %d backup peers accepted so far, %d still needed",
					backupAcceptedCount,
					stillNeeded,
				)
			}

			// If we've exhausted all backup peers and still don't have enough, break
			if backupPeersIndex >= len(allBackupPeers) && stillNeeded > 0 {
				log.Printf("Exhausted all %d backup peers, still need %d more subscribers", len(allBackupPeers), stillNeeded)
				break
			}
		}

		finalPeers := append(append([]peer.ID{}, mainAcceptedPeers...), backupAcceptedPeers...)
		totalAccepted := len(finalPeers)
		log.Printf(
			"Final subscription results: %d main + %d backup = %d total subscribers",
			mainAcceptedCount,
			backupAcceptedCount,
			totalAccepted,
		)

		// Ensure we have exactly MaxMainPeers subscribers
		if totalAccepted != config.MaxMainPeers {
			// Get accepted peer IDs from tracker
			acceptedPeers := tracker.GetBuddyNodes()

			// Format peer IDs with each on a new line (same format as Consensus.go, for easy copy-paste)
			var peerIDStrings []string
			for peerID := range acceptedPeers {
				peerIDStrings = append(peerIDStrings, fmt.Sprintf("  - %s", peerID.String()))
			}
			peerIDsStr := strings.Join(peerIDStrings, "\n")

			return fmt.Errorf("insufficient subscribers for consensus: got %d, need exactly %d MaxMainPeers (1 creator + MaxMainPeers subscribers).\nAccepted peer IDs:\n%s", totalAccepted, config.MaxMainPeers, peerIDsStr)
		}

		log.Printf("Successfully achieved consensus: 1 creator + %d MaxMainPeers subscribers", totalAccepted)
		setFinalConsensusPeers(consensus, finalPeers)

		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())

			// Cleanup response handler channels now that we're done
			cleanupResponseHandler(responseHandler)

			return nil
		}
	}

	// Check if we got exactly the required number of main peers (no backups needed)
	if mainAcceptedCount == config.MaxMainPeers {
		log.Printf("Successfully achieved consensus with %d main peers (no backups needed)", mainAcceptedCount)

		// Verify with global tracker
		if tracker.HasRequiredSubscriptions(config.MaxMainPeers) {
			log.Printf("Global tracker confirms: %d active subscriptions", tracker.GetActiveCount())
			setFinalConsensusPeers(consensus, mainAcceptedPeers)
			cleanupResponseHandler(responseHandler)
			return nil
		}
	}

	// Cleanup response handler before returning error
	cleanupResponseHandler(responseHandler)

	return fmt.Errorf("global tracker validation failed: got %d, need %d", tracker.GetActiveCount(), config.MaxMainPeers)
}

func setFinalConsensusPeers(consensus *Consensus, finalPeers []peer.ID) {
	if consensus == nil {
		return
	}
	if len(finalPeers) != config.MaxMainPeers {
		// Defensive: only set when we have the exact final committee size.
		return
	}

	if consensus.mu != nil {
		consensus.mu.Lock()
		defer consensus.mu.Unlock()
	}

	consensus.PeerList.MainPeers = append([]peer.ID{}, finalPeers...)
	// Keep BackupPeers as-is (they're still allowed peers for the channel), but the
	// "committee" used for later verification must be exactly MainPeers.
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
func askPeersForSubscription(
	Listener *MessagePassing.StructListener,
	topic string,
	peerAddrs []peer.ID,
	responseHandler *ResponseHandler,
	peerType string,
) (int, int, []peer.ID) {
	fmt.Println("askPeersForSubscription", peerAddrs)
	if len(peerAddrs) == 0 {
		log.Printf("No %s peers to ask for subscription", peerType)
		return 0, 0, nil
	}

	// Get initial tracker count before this call
	tracker := PubSubMessages.GetSubscriptionTracker()
	initialCount := tracker.GetActiveCount()

	accepted := make(map[string]bool)
	var mu sync.Mutex

	log.Printf("Asking %d %s peers for subscription to topic: %s", len(peerAddrs), peerType, topic)
	log.Printf("Initial tracker count: %d", initialCount)

	// Get host from GossipPubSub (not create BuddyNode yet)
	host := Listener.ListenerBuddyNode.Host

	// Response handler is now set up in the buddy nodes themselves
	// No need to set up additional handlers here
	wg, err := common.LocalGRO.NewFunctionWaitGroup(context.Background(), GRO.SequencerConsensusThread)
	if err != nil {
		log.Printf("Failed to create waitgroup: %v", err)
		return 0, 0, nil
	}

	// Track how many goroutines we successfully spawned
	spawnedCount := 0

	for _, peerID := range peerAddrs {
		// Register peer for response tracking
		responseChan := responseHandler.RegisterPeer(peerID, peerType)

		// Capture variables in closure to avoid race condition
		peerID := peerID
		// responseChan is already a new variable per iteration, but capture explicitly for clarity
		chanForGoroutine := responseChan
		if err := common.LocalGRO.Go(GRO.SequencerConsensusThread, func(ctx context.Context) error {
			// DON'T unregister here - keep the role stored for HandleResponse
			// defer responseHandler.UnregisterPeer(peerID)

			// Create context with timeout derived from parent context
			timeoutCtx, cancel := context.WithTimeout(ctx, subscriptionAckTimeout)
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
				return fmt.Errorf("failed to marshal subscription request message: %v", err)
			}

			// Send subscription request via SubmitMessageProtocol using existing function
			if err := Listener.SendMessageToPeer(peerID, string(messageBytes)); err != nil {
				log.Printf("Failed to send subscription request to %s %s: %v", peerType, peerID, err)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
				return fmt.Errorf("failed to send subscription request to %s %s: %v", peerType, peerID, err)
			}

			log.Printf("Sent subscription request to %s peer: %s, waiting for ACK...", peerType, peerID)
			fmt.Printf("=== askPeersForSubscription: Waiting for response from peer: %s ===\n", peerID)
			fmt.Printf("=== askPeersForSubscription: Response channel: %p for peer: %s ===\n", chanForGoroutine, peerID)

			// Wait for response with timeout
			select {
			case response := <-chanForGoroutine:
				fmt.Printf("=== askPeersForSubscription: Received response from peer: %s (accepted: %t) ===\n", peerID, response)
				mu.Lock()
				accepted[peerID.String()] = response
				mu.Unlock()

				if response {
					log.Printf("%s peer %s accepted subscription", peerType, peerID)
				} else {
					log.Printf("%s peer %s rejected subscription", peerType, peerID)
				}
			case <-timeoutCtx.Done():
				fmt.Printf("=== askPeersForSubscription: TIMEOUT waiting for response from peer: %s ===\n", peerID)
				fmt.Printf("=== askPeersForSubscription: Timeout context done for peer: %s ===\n", peerID)
				log.Printf("Timeout waiting for ACK from %s peer: %s", peerType, peerID)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
			}
			return nil
		}, local.AddToWaitGroup(GRO.SequencerConsensusThread)); err != nil {
			log.Printf("Failed to spawn goroutine for peer %s: %v", peerID, err)
			// Mark as not accepted if we couldn't even spawn the goroutine
			mu.Lock()
			accepted[peerID.String()] = false
			mu.Unlock()
			continue
		}
		spawnedCount++
	}

	// Wait for all goroutines to complete
	wg.Wait()

	log.Printf("All %d goroutines completed for %s peers", spawnedCount, peerType)

	// Give some time for late-arriving responses to be processed
	time.Sleep(subscriptionLateResponseBuffer)

	// CRITICAL FIX: Sync accepted map with tracker after late response buffer.
	// Late responses may have updated the tracker but not the accepted map (if they
	// arrived after the goroutine timed out). Use tracker as single source of truth.
	trackerPeers := tracker.GetBuddyNodes()
	for _, pid := range peerAddrs {
		// If tracker says this peer was accepted, ensure it's in the accepted map
		if role, exists := trackerPeers[pid]; exists && role == peerType {
			mu.Lock()
			accepted[pid.String()] = true
			mu.Unlock()
		}
	}

	// Calculate how many NEW peers were accepted in this call
	finalCount := tracker.GetActiveCount()
	newAccepted := finalCount - initialCount

	log.Printf("Tracker count: %d -> %d (new: %d) for %s peers", initialCount, finalCount, newAccepted, peerType)

	// Collect accepted peers in a stable order (same order as peerAddrs).
	// Now synced with tracker, so includes late responses.
	acceptedPeers := make([]peer.ID, 0, len(peerAddrs))
	for _, pid := range peerAddrs {
		if accepted[pid.String()] {
			acceptedPeers = append(acceptedPeers, pid)
		}
	}

	// Verify consistency: acceptedPeers count should match tracker delta
	if len(acceptedPeers) != newAccepted {
		log.Printf("⚠️ WARNING: acceptedPeers count (%d) != tracker delta (%d) for %s peers - using tracker as source of truth", len(acceptedPeers), newAccepted, peerType)
		// Rebuild acceptedPeers from tracker to ensure consistency
		acceptedPeers = make([]peer.ID, 0, newAccepted)
		for _, pid := range peerAddrs {
			if role, exists := trackerPeers[pid]; exists && role == peerType {
				acceptedPeers = append(acceptedPeers, pid)
			}
		}
	}

	// Note: Don't cleanup channels here - they're still being used
	// Cleanup will happen when the next batch starts or when consensus completes
	return newAccepted, len(peerAddrs), acceptedPeers
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
	// if len(consensus.PeerList.BackupPeers) > 0 && len(consensus.PeerList.BackupPeers) != config.MaxBackupPeers {
	// 	return fmt.Errorf("if backup peers are set, count must be exactly %d, got %d", config.MaxBackupPeers, len(consensus.PeerList.BackupPeers))
	// }

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
