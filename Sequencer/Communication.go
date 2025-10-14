package Sequencer

import (
	"context"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// AskForSubscription asks all connected peers if they want to subscribe to a topic
func AskForSubscription(gps *Pubsub.GossipPubSub, topic string) error {
	Accepted := make(map[string]bool)
	var wg sync.WaitGroup
	var mu sync.Mutex

	peers := gps.GetPeers()
	log.Printf("Asking %d peers for subscription to topic: %s", len(peers), topic)

	// Create a BuddyNode from the GossipPubSub's host to use its SendMessage method
	buddy := MessagePassing.NewBuddyNode(gps.Host, &MessagePassing.Buddies{})

	// Create a response channel to collect ACK responses
	responseChan := make(chan struct {
		peerID   peer.ID
		accepted bool
	}, len(peers))

	for _, peerAddr := range peers {
		wg.Add(1)
		go func(peerID peer.ID) {
			defer wg.Done()

			// Create context with 4-second timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Send ASK_FOR_SUBSCRIPTION message to the peer
			if err := buddy.SendMessageToPeer(peerID, config.Type_AskForSubscription); err != nil {
				log.Printf("Failed to send ASK_FOR_SUBSCRIPTION to %s: %v", peerID, err)
				responseChan <- struct {
					peerID   peer.ID
					accepted bool
				}{peerID, false}
				return
			}

			log.Printf("Sent subscription request to peer: %s, waiting for ACK...", peerID)

			// Wait for ACK response with timeout
			// Note: This is a simplified implementation. In a real scenario, you would need to:
			// 1. Set up a response handler that listens for incoming ACK messages
			// 2. Use a channel or callback mechanism to receive the actual ACK response
			// 3. Parse the response to determine if it's ACK_TRUE or ACK_FALSE

			// For now, we'll simulate waiting for a response and then timeout
			// In practice, you would replace this with actual response handling
			select {	
			case <-ctx.Done():
				log.Printf("Timeout waiting for ACK from peer: %s", peerID)
				responseChan <- struct {
					peerID   peer.ID
					accepted bool
				}{peerID, false}
			case <-time.After(5 * time.Second):
				log.Printf("Timeout waiting for ACK from peer: %s", peerID)
				responseChan <- struct {
					peerID   peer.ID
					accepted bool
				}{peerID, false}
			}
		}(peerAddr)
	}

	// Wait for all goroutines to complete and collect responses
	go func() {
		wg.Wait()
		close(responseChan)
	}()

	// Collect all responses
	for response := range responseChan {
		mu.Lock()
		Accepted[response.peerID.String()] = response.accepted
		mu.Unlock()

		if response.accepted {
			log.Printf("Peer %s accepted subscription", response.peerID)
		} else {
			log.Printf("Peer %s rejected or timed out", response.peerID)
		}
	}

	acceptedCount := 0
	for _, accepted := range Accepted {
		if accepted {
			acceptedCount++
		}
	}

	log.Printf("Subscription results: %d peers accepted out of %d", acceptedCount, len(peers))
	return nil
}
