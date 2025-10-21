package Sequencer

import (
	"context"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ResponseHandler manages ACK responses from peers
type ResponseHandler struct {
	responses map[peer.ID]chan bool
	peerIDs   map[peer.ID]string // Store the actual PeerID from ACK responses
	mutex     sync.RWMutex
}

// NewResponseHandler creates a new response handler
func NewResponseHandler() *ResponseHandler {
	return &ResponseHandler{
		responses: make(map[peer.ID]chan bool),
		peerIDs:   make(map[peer.ID]string),
	}
}

// RegisterPeer registers a peer for response tracking
func (rh *ResponseHandler) RegisterPeer(peerID peer.ID) chan bool {
	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	responseChan := make(chan bool, 1)
	rh.responses[peerID] = responseChan
	return responseChan
}

// HandleResponse handles an ACK response from a peer
func (rh *ResponseHandler) HandleResponse(peerID peer.ID, accepted bool) {
	rh.mutex.RLock()
	responseChan, exists := rh.responses[peerID]
	rh.mutex.RUnlock()

	if exists {
		select {
		case responseChan <- accepted:
		default:
			// Channel is full or closed, ignore
		}
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

	// Also clean up the peerIDs map
	delete(rh.peerIDs, peerID)
}

// AskForSubscription asks peers for subscription with backup node fallback
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func AskForSubscription(sgps *Pubsub.StructGossipPubSub, topic string, consensus *Consensus) error {
	responseHandler := NewResponseHandler()

	// First, try main peers (up to 13)
	mainAccepted, mainTotal := askPeersForSubscription(sgps, topic, consensus.PeerList.MainPeers, responseHandler, "main")
	mainFailed := mainTotal - mainAccepted

	log.Printf("Main peers results: %d accepted, %d failed out of %d", mainAccepted, mainFailed, mainTotal)

	// Check if more than 3 main nodes failed
	if mainFailed > MaxBackupPeers {
		return fmt.Errorf("too many main nodes failed: %d failed, maximum allowed is %d", mainFailed, MaxBackupPeers)
	}

	// If we have exactly 13 main peers, we're done
	if mainAccepted == MaxMainPeers {
		log.Printf("Perfect! Got exactly %d main peers for consensus (1 creator + 13 subscribers = 14 total)", MaxMainPeers)
		return nil
	}

	// If we have less than 13 main peers, use backup peers as replacements
	if mainAccepted < MaxMainPeers {
		needed := MaxMainPeers - mainAccepted
		log.Printf("Need %d backup nodes as replacements for failed main nodes", needed)

		// Limit backup peers to only what we need (max 3)
		backupPeersToTry := consensus.PeerList.BackupPeers
		if len(backupPeersToTry) > needed {
			backupPeersToTry = backupPeersToTry[:needed]
		}

		backupAccepted, backupTotal := askPeersForSubscription(sgps, topic, backupPeersToTry, responseHandler, "backup")

		log.Printf("Backup peers results: %d accepted out of %d tried", backupAccepted, backupTotal)

		totalAccepted := mainAccepted + backupAccepted

		log.Printf("Final subscription results: %d main + %d backup = %d total subscribers", mainAccepted, backupAccepted, totalAccepted)

		// Ensure we have exactly 13 subscribers (1 creator + 13 subscribers = 14 total)
		if totalAccepted != MaxMainPeers {
			return fmt.Errorf("insufficient subscribers for consensus: got %d, need exactly %d (1 creator + 13 subscribers = 14 total)", totalAccepted, MaxMainPeers)
		}

		log.Printf("Successfully achieved consensus: 1 creator + %d subscribers = 14 total nodes", totalAccepted)
	}

	return nil
}

// VerifySubscriptions publishes a verification message to the pubsub channel and collects ACK responses
// All subscribed nodes should reply with ACK_TRUE status and their PeerID
func VerifySubscriptions(sgps *Pubsub.StructGossipPubSub, consensus *Consensus) (map[peer.ID]string, error) {
	// Create a channel to collect verification responses
	verificationResponses := make(map[peer.ID]string)
	var mu sync.Mutex

	// Expected number of responses (13 main peers)
	expectedResponses := len(consensus.PeerList.MainPeers)
	responseCount := 0
	timeout := 10 * time.Second

	log.Printf("Starting pubsub-based subscription verification for %d main peers", expectedResponses)

	// Subscribe to the consensus channel to receive verification responses
	handler := func(msg *Struct.GossipMessage) {
		// Check if message has ACK data
		if msg.Data != nil && msg.Data.ACK != nil {
			// Check if this is a verification response with all required fields
			if msg.Data.ACK.Status == config.Type_ACK_True && msg.Data.ACK.Stage == config.Type_VerifySubscription {
				// Parse peer ID
				peerID, err := peer.Decode(msg.Data.ACK.PeerID)
				if err != nil {
					log.Printf("Failed to decode peer ID: %v", err)
					return
				}

				// Check if this peer is in our main peers list
				isMainPeer := false
				for _, mainPeer := range consensus.PeerList.MainPeers {
					if mainPeer == peerID {
						isMainPeer = true
						break
					}
				}

				if isMainPeer {
					mu.Lock()
					verificationResponses[peerID] = msg.Data.ACK.PeerID
					responseCount++
					log.Printf("Received verification ACK from main peer: %s", peerID)
					mu.Unlock()
				} else {
					log.Printf("Received verification ACK from non-main peer: %s (ignoring)", peerID)
				}
			}
		}
	}

	// Subscribe to the consensus channel
	if err := sgps.Subscribe(consensus.Channel, handler); err != nil {
		return nil, fmt.Errorf("failed to subscribe to consensus channel for verification: %v", err)
	}


	var message string = "Please verify your subscription to the consensus channel"

	ACK_MESSAGE := Structs.NewACKBuilder().True_ACK_Message(sgps.GetGossipPubSub().Host.ID(), config.Type_VerifySubscription).GetACK_Message()

	verificationMessage := Structs.NewMessageBuilder().SetMessage(message).SetSender(sgps.GetGossipPubSub().Host.ID()).SetTimestamp(time.Now().Unix()).SetACK(ACK_MESSAGE).GetMessage()
	if err := sgps.Publish(consensus.Channel, verificationMessage, map[string]string{}); err != nil {
		return nil, fmt.Errorf("failed to publish verification message: %v", err)
	}

	log.Printf("Published verification request to pubsub channel: %s", consensus.Channel)

	// Wait for responses with timeout
	startTime := time.Now()
	for time.Since(startTime) < timeout {
		mu.Lock()
		currentCount := responseCount
		mu.Unlock()

		if currentCount >= expectedResponses {
			log.Printf("Received all expected verification responses: %d/%d", currentCount, expectedResponses)
			break
		}

		// Check every 100ms
		time.Sleep(100 * time.Millisecond)
	}

	// Final count
	mu.Lock()
	finalCount := responseCount
	mu.Unlock()

	log.Printf("Subscription verification completed: %d peers verified out of %d expected", finalCount, expectedResponses)

	// Unsubscribe from the channel
	if err := sgps.Unsubscribe(consensus.Channel); err != nil {
		log.Printf("Warning: failed to unsubscribe from consensus channel: %v", err)
	}

	return verificationResponses, nil
}

// askPeersForSubscription asks a list of peers for subscription
func askPeersForSubscription(sgps *Pubsub.StructGossipPubSub, topic string, peerAddrs []peer.ID, responseHandler *ResponseHandler, peerType string) (int, int) {
	if len(peerAddrs) == 0 {
		log.Printf("No %s peers to ask for subscription", peerType)
		return 0, 0
	}

	accepted := make(map[string]bool)
	var wg sync.WaitGroup
	var mu sync.Mutex

	log.Printf("Asking %d %s peers for subscription to topic: %s", len(peerAddrs), peerType, topic)

	// Create a BuddyNode from the GossipPubSub's host with response handler
	buddy := MessagePassing.NewBuddyNode(sgps.GetGossipPubSub().GetHost(), &Structs.Buddies{Buddies_Nodes: peerAddrs}, responseHandler, sgps.GetGossipPubSub())

	for _, peerID := range peerAddrs {
		// Register peer for response tracking
		responseChan := responseHandler.RegisterPeer(peerID)

		wg.Add(1)
		go func(peerID peer.ID) {
			defer wg.Done()
			defer responseHandler.UnregisterPeer(peerID)

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Send subscription request
			if err := MessagePassing.NewStructBuddyNode(buddy).SendMessageToPeer(peerID, config.Type_AskForSubscription); err != nil {
				log.Printf("Failed to send subscription request to %s %s: %v", peerType, peerID, err)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
				return
			}

			log.Printf("Sent subscription request to %s peer: %s, waiting for ACK...", peerType, peerID)

			// Wait for response with timeout
			select {
			case response := <-responseChan:
				mu.Lock()
				accepted[peerID.String()] = response
				mu.Unlock()

				if response {
					log.Printf("%s peer %s accepted subscription", peerType, peerID)
				} else {
					log.Printf("%s peer %s rejected subscription", peerType, peerID)
				}
			case <-ctx.Done():
				log.Printf("Timeout waiting for ACK from %s peer: %s", peerType, peerID)
				mu.Lock()
				accepted[peerID.String()] = false
				mu.Unlock()
			}
		}(peerID)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Count accepted peers
	acceptedCount := 0
	for _, isAccepted := range accepted {
		if isAccepted {
			acceptedCount++
		}
	}

	return acceptedCount, len(peerAddrs)
}

// ValidateConsensusConfiguration validates that the consensus configuration is correct
// Architecture: 1 creator + 13 main peers + 3 backup peers = 17 total allowed peers
// Active consensus: 1 creator + 13 main peers = 14 active participants
// Backup peers are standby replacements, not active participants
func ValidateConsensusConfiguration(consensus *Consensus) error {
	// Check main peers count (should be 13)
	if len(consensus.PeerList.MainPeers) != MaxMainPeers {
		return fmt.Errorf("main peers count must be exactly %d, got %d", MaxMainPeers, len(consensus.PeerList.MainPeers))
	}

	// Check backup peers count (should be 3)
	if len(consensus.PeerList.BackupPeers) != MaxBackupPeers {
		return fmt.Errorf("backup peers count must be exactly %d, got %d", MaxBackupPeers, len(consensus.PeerList.BackupPeers))
	}

	// Check for duplicate peer IDs within main peers
	if err := checkForDuplicatePeerIDs(consensus.PeerList.MainPeers); err != nil {
		return fmt.Errorf("duplicate peer ID found in main peers: %w", err)
	}

	// Check for duplicate peer IDs within backup peers
	if err := checkForDuplicatePeerIDs(consensus.PeerList.BackupPeers); err != nil {
		return fmt.Errorf("duplicate peer ID found in backup peers: %w", err)
	}

	// Check for duplicate peer IDs between main and backup peers
	allPeers := make(map[peer.ID]bool)

	// Add main peers to the map
	for _, peerID := range consensus.PeerList.MainPeers {
		allPeers[peerID] = true
	}

	// Check backup peers against main peers
	for _, peerID := range consensus.PeerList.BackupPeers {
		if allPeers[peerID] {
			return fmt.Errorf("duplicate peer ID found between main and backup peers: %s", peerID)
		}
		allPeers[peerID] = true
	}

	// Check total peers (should be 16: 13 main + 3 backup)
	totalPeers := len(consensus.PeerList.MainPeers) + len(consensus.PeerList.BackupPeers)
	expectedTotal := MaxMainPeers + MaxBackupPeers
	if totalPeers != expectedTotal {
		return fmt.Errorf("total peers count must be exactly %d (13 main + 3 backup), got %d", expectedTotal, totalPeers)
	}

	log.Printf("Consensus configuration validated: %d main peers, %d backup peers (1 creator + 13 active + 3 standby = 17 total allowed)",
		len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

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
