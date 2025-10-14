package Sequencer

import (
	"context"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
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
	mutex     sync.RWMutex
}

// NewResponseHandler creates a new response handler
func NewResponseHandler() *ResponseHandler {
	return &ResponseHandler{
		responses: make(map[peer.ID]chan bool),
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

// UnregisterPeer removes a peer from response tracking
func (rh *ResponseHandler) UnregisterPeer(peerID peer.ID) {
	rh.mutex.Lock()
	defer rh.mutex.Unlock()

	if responseChan, exists := rh.responses[peerID]; exists {
		close(responseChan)
		delete(rh.responses, peerID)
	}
}

// AskForSubscription asks peers for subscription with backup node fallback
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func AskForSubscription(gps *Pubsub.GossipPubSub, topic string, consensus *Consensus) error {
	responseHandler := NewResponseHandler()

	// First, try main peers (up to 13)
	mainAccepted, mainTotal := askPeersForSubscription(gps, topic, consensus.PeerList.MainPeers, responseHandler, "main")
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

		backupAccepted, backupTotal := askPeersForSubscription(gps, topic, backupPeersToTry, responseHandler, "backup")

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

// askPeersForSubscription asks a list of peers for subscription
func askPeersForSubscription(gps *Pubsub.GossipPubSub, topic string, peerAddrs []peer.ID, responseHandler *ResponseHandler, peerType string) (int, int) {
	if len(peerAddrs) == 0 {
		log.Printf("No %s peers to ask for subscription", peerType)
		return 0, 0
	}

	accepted := make(map[string]bool)
	var wg sync.WaitGroup
	var mu sync.Mutex

	log.Printf("Asking %d %s peers for subscription to topic: %s", len(peerAddrs), peerType, topic)

	// Create a BuddyNode from the GossipPubSub's host with response handler
	buddy := MessagePassing.NewBuddyNode(gps.Host, &MessagePassing.Buddies{}, responseHandler)

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
			if err := buddy.SendMessageToPeer(peerID, config.Type_AskForSubscription); err != nil {
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
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func ValidateConsensusConfiguration(consensus *Consensus) error {
	// Check main peers count (should be 13)
	if len(consensus.PeerList.MainPeers) != MaxMainPeers {
		return fmt.Errorf("main peers count must be exactly %d, got %d", MaxMainPeers, len(consensus.PeerList.MainPeers))
	}

	// Check backup peers count (should be 3)
	if len(consensus.PeerList.BackupPeers) != MaxBackupPeers {
		return fmt.Errorf("backup peers count must be exactly %d, got %d", MaxBackupPeers, len(consensus.PeerList.BackupPeers))
	}

	// Check for duplicate peer IDs between main and backup
	allPeers := make(map[peer.ID]bool)

	for _, peerID := range consensus.PeerList.MainPeers {
		if allPeers[peerID] {
			return fmt.Errorf("duplicate peer ID found in main peers: %s", peerID)
		}
		allPeers[peerID] = true
	}

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

	log.Printf("Consensus configuration validated: %d main peers, %d backup peers (1 creator + 13 subscribers = 14 total nodes)",
		len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

	return nil
}
