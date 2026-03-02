package bft

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"jmdn/Pubsub"
	Publisher "jmdn/Pubsub/Publish"
	Subscription "jmdn/Pubsub/Subscription"
	"jmdn/config"
	"jmdn/config/PubSubMessages"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// BFTPubSubAdapter integrates BFT with GossipSub
type BFTPubSubAdapter struct {
	gps         *PubSubMessages.GossipPubSub
	bftEngine   *BFT
	channelName string

	// Vote channels per round
	prepareVotes map[string]chan *PrepareMessage
	commitVotes  map[string]chan *CommitMessage
	votesMutex   sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewBFTPubSubAdapter creates adapter
func NewBFTPubSubAdapter(
	ctx context.Context,
	gps *PubSubMessages.GossipPubSub,
	bftEngine *BFT,
	channelName string,
) (*BFTPubSubAdapter, error) {

	adapterCtx, cancel := context.WithCancel(ctx)

	adapter := &BFTPubSubAdapter{
		gps:          gps,
		bftEngine:    bftEngine,
		channelName:  channelName,
		prepareVotes: make(map[string]chan *PrepareMessage),
		commitVotes:  make(map[string]chan *CommitMessage),
		ctx:          adapterCtx,
		cancel:       cancel,
	}

	// Create consensus channel
	err := Pubsub.CreateChannel(gps, channelName, false, []peer.ID{})
	if err != nil {
		log.Printf("⚠️  Channel might already exist: %v", err)
	}

	log.Printf("✅ BFT PubSub Adapter initialized")
	return adapter, nil
}

// ProposeConsensus runs BFT consensus
func (adapter *BFTPubSubAdapter) ProposeConsensus(
	ctx context.Context,
	round uint64,
	blockHash string,
	myBuddyID string,
	allBuddies []BuddyInput,
) (*Result, error) {

	roundID := fmt.Sprintf("%d-%s", round, blockHash[:min(8, len(blockHash))])

	// log.Printf("\n" + "="*80)
	log.Printf("🚀 STARTING BFT CONSENSUS")
	// log.Printf("=" * 80)
	log.Printf("   Round ID: %s", roundID)
	log.Printf("   Buddies: %d", len(allBuddies))
	log.Printf("   Threshold: 2f+1 = %d", adapter.bftEngine.CalculateThreshold(len(allBuddies)))
	// log.Printf("="*80 + "\n")

	// Register vote channels
	adapter.votesMutex.Lock()
	adapter.prepareVotes[roundID] = make(chan *PrepareMessage, 100)
	adapter.commitVotes[roundID] = make(chan *CommitMessage, 100)
	adapter.votesMutex.Unlock()

	defer func() {
		adapter.votesMutex.Lock()
		close(adapter.prepareVotes[roundID])
		close(adapter.commitVotes[roundID])
		delete(adapter.prepareVotes, roundID)
		delete(adapter.commitVotes, roundID)
		adapter.votesMutex.Unlock()
	}()

	// Register buddy public keys
	for _, buddy := range allBuddies {
		RegisterPublicKey(buddy.ID, buddy.PublicKey)
	}

	// Broadcast START_PUBSUB
	if err := adapter.broadcastStartPubSub(roundID, allBuddies); err != nil {
		return nil, fmt.Errorf("failed to broadcast START_PUBSUB: %w", err)
	}

	// Create messenger
	messenger := &pubsubMessenger{
		adapter:     adapter,
		roundID:     roundID,
		prepareChan: adapter.prepareVotes[roundID],
		commitChan:  adapter.commitVotes[roundID],
	}

	// Run BFT
	result, err := adapter.bftEngine.RunConsensus(ctx, round, blockHash, myBuddyID, allBuddies, messenger, nil)

	// Broadcast END
	success := err == nil && result != nil && result.Success
	adapter.broadcastEndPubSub(roundID, success)

	if err != nil {
		log.Printf("\n❌ CONSENSUS FAILED: %v\n", err)
		return result, err
	}

	log.Printf("\n✅ CONSENSUS SUCCESS - Decision: %s\n", result.Decision)
	return result, nil
}

// These methods are called by SubscriptionService
func (adapter *BFTPubSubAdapter) HandleStartPubSub(msg *PubSubMessages.GossipMessage) error {
	log.Printf("📢 BFT: START_PUBSUB for round %s", msg.Data.RoundID)
	return nil
}

func (adapter *BFTPubSubAdapter) HandleEndPubSub(msg *PubSubMessages.GossipMessage) error {
	log.Printf("🏁 BFT: END_PUBSUB for round %s - unsubscribing from %s", msg.Data.RoundID, adapter.channelName)

	// CRITICAL FIX: Unsubscribe from consensus channel to prevent resource accumulation
	// Without this, subscriptions accumulate over time causing goroutine leaks and stream reset errors
	if err := Subscription.Unsubscribe(adapter.gps, adapter.channelName); err != nil {
		log.Printf("⚠️ Failed to unsubscribe from %s: %v (non-fatal)", adapter.channelName, err)
		// Don't return error - this is cleanup, failure shouldn't stop processing
	}

	// Log subscription stats for monitoring
	if manager := Subscription.GetSubscriptionManager(adapter.gps); manager != nil {
		stats := manager.GetStats()
		log.Printf("📊 Subscription stats after EndPubSub: total=%v, refCount=%v",
			stats["total_subscriptions"], stats["total_ref_count"])
	}

	return nil
}

func (adapter *BFTPubSubAdapter) HandlePrepareVote(msg *PubSubMessages.GossipMessage) error {
	roundID := msg.Data.RoundID

	var prepareMsg PrepareMessage
	if err := json.Unmarshal(msg.Data.VoteData, &prepareMsg); err != nil {
		return err
	}

	log.Printf("   📥 PREPARE from %s: %s", prepareMsg.BuddyID[:min(16, len(prepareMsg.BuddyID))], prepareMsg.Decision)

	adapter.votesMutex.RLock()
	voteChan, exists := adapter.prepareVotes[roundID]
	adapter.votesMutex.RUnlock()

	if exists {
		select {
		case voteChan <- &prepareMsg:
		default:
		}
	}
	return nil
}

func (adapter *BFTPubSubAdapter) HandleCommitVote(msg *PubSubMessages.GossipMessage) error {
	roundID := msg.Data.RoundID

	var commitMsg CommitMessage
	if err := json.Unmarshal(msg.Data.VoteData, &commitMsg); err != nil {
		return err
	}

	log.Printf("   📥 COMMIT from %s: %s", commitMsg.BuddyID[:min(16, len(commitMsg.BuddyID))], commitMsg.Decision)

	adapter.votesMutex.RLock()
	voteChan, exists := adapter.commitVotes[roundID]
	adapter.votesMutex.RUnlock()

	if exists {
		select {
		case voteChan <- &commitMsg:
		default:
		}
	}
	return nil
}

func (adapter *BFTPubSubAdapter) broadcastStartPubSub(roundID string, buddies []BuddyInput) error {
	buddyIDs := make([]peer.ID, len(buddies))
	for i, buddy := range buddies {
		buddyIDs[i] = peer.ID(buddy.ID)
	}

	msg := &PubSubMessages.GossipMessage{
		ID:        uuid.New().String(),
		Topic:     adapter.channelName,
		Sender:    adapter.gps.Host.ID(),
		TTL:       config.MaxHops,
		Timestamp: time.Now().UTC().Unix(),
		Data: &PubSubMessages.Message{
			Sender:    adapter.gps.Host.ID(),
			Message:   "",
			Timestamp: time.Now().UTC().Unix(),
			ACK: &PubSubMessages.ACK{
				Status: "start",
				PeerID: adapter.gps.Host.ID().String(),
				Stage:  config.Type_StartPubSub,
			},
			RoundID:      roundID,
			BuddyNodeIDs: buddyIDs,
		},
	}

	// Serialize and gossip
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal START_PUBSUB: %w", err)
	}

	// Add to cache to prevent loop
	adapter.gps.Mutex.Lock()
	adapter.gps.MessageCache[msg.ID] = time.Now()
	adapter.gps.Mutex.Unlock()

	// Use GossipMessage (lowercase 'g')
	Publisher.GossipMessage(adapter.gps, msgBytes)
	return nil
}

func (adapter *BFTPubSubAdapter) broadcastEndPubSub(roundID string, success bool) error {
	msg := &PubSubMessages.GossipMessage{
		ID:        uuid.New().String(),
		Topic:     adapter.channelName,
		Sender:    adapter.gps.Host.ID(),
		TTL:       config.MaxHops,
		Timestamp: time.Now().UTC().Unix(),
		Data: &PubSubMessages.Message{
			Sender:    adapter.gps.Host.ID(),
			Message:   "",
			Timestamp: time.Now().UTC().Unix(),
			ACK: &PubSubMessages.ACK{
				Status: "end",
				PeerID: adapter.gps.Host.ID().String(),
				Stage:  config.Type_EndPubSub,
			},
			RoundID:          roundID,
			ConsensusSuccess: success,
		},
	}

	// Serialize and gossip
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal END_PUBSUB: %w", err)
	}

	// Add to cache to prevent loop
	adapter.gps.Mutex.Lock()
	adapter.gps.MessageCache[msg.ID] = time.Now()
	adapter.gps.Mutex.Unlock()

	// Use GossipMessage (lowercase 'g')
	Publisher.GossipMessage(adapter.gps, msgBytes)
	return nil
}

func (adapter *BFTPubSubAdapter) Close() error {
	adapter.cancel()
	return nil
}

// pubsubMessenger implements Messenger
type pubsubMessenger struct {
	adapter     *BFTPubSubAdapter
	roundID     string
	prepareChan <-chan *PrepareMessage
	commitChan  <-chan *CommitMessage
}

func (pm *pubsubMessenger) BroadcastPrepare(msg *PrepareMessage) error {
	voteData, _ := json.Marshal(msg)

	gossipMsg := &PubSubMessages.GossipMessage{
		ID:        uuid.New().String(),
		Topic:     pm.adapter.channelName,
		Sender:    pm.adapter.gps.Host.ID(),
		TTL:       config.MaxHops,
		Timestamp: time.Now().UTC().Unix(),
		Data: &PubSubMessages.Message{
			Sender:    pm.adapter.gps.Host.ID(),
			Message:   "",
			Timestamp: time.Now().UTC().Unix(),
			ACK: &PubSubMessages.ACK{
				Status: "prepare",
				PeerID: pm.adapter.gps.Host.ID().String(),
				Stage:  config.Type_Publish,
			},
			RoundID:   pm.roundID,
			Phase:     "PREPARE",
			VoteData:  voteData,
			VoteValue: msg.Decision == Accept,
		},
	}

	log.Printf("   📤 PREPARE: %s", msg.Decision)

	// Serialize and gossip
	msgBytes, err := json.Marshal(gossipMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal PREPARE: %w", err)
	}

	// Add to cache to prevent loop
	pm.adapter.gps.Mutex.Lock()
	pm.adapter.gps.MessageCache[gossipMsg.ID] = time.Now()
	pm.adapter.gps.Mutex.Unlock()

	// Use GossipMessage (lowercase 'g')
	Publisher.GossipMessage(pm.adapter.gps, msgBytes)
	return nil
}

func (pm *pubsubMessenger) BroadcastCommit(msg *CommitMessage) error {
	voteData, _ := json.Marshal(msg)

	gossipMsg := &PubSubMessages.GossipMessage{
		ID:        uuid.New().String(),
		Topic:     pm.adapter.channelName,
		Sender:    pm.adapter.gps.Host.ID(),
		TTL:       config.MaxHops,
		Timestamp: time.Now().UTC().Unix(),
		Data: &PubSubMessages.Message{
			Sender:    pm.adapter.gps.Host.ID(),
			Message:   "",
			Timestamp: time.Now().UTC().Unix(),
			ACK: &PubSubMessages.ACK{
				Status: "commit",
				PeerID: pm.adapter.gps.Host.ID().String(),
				Stage:  config.Type_Publish,
			},
			RoundID:   pm.roundID,
			Phase:     "COMMIT",
			VoteData:  voteData,
			VoteValue: msg.Decision == Accept,
		},
	}

	log.Printf("   📤 COMMIT: %s", msg.Decision)

	// Serialize and gossip
	msgBytes, err := json.Marshal(gossipMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal COMMIT: %w", err)
	}

	// Add to cache to prevent loop
	pm.adapter.gps.Mutex.Lock()
	pm.adapter.gps.MessageCache[gossipMsg.ID] = time.Now()
	pm.adapter.gps.Mutex.Unlock()

	// Use GossipMessage (lowercase 'g')
	Publisher.GossipMessage(pm.adapter.gps, msgBytes)
	return nil
}

func (pm *pubsubMessenger) ReceivePrepare() <-chan *PrepareMessage {
	return pm.prepareChan
}

func (pm *pubsubMessenger) ReceiveCommit() <-chan *CommitMessage {
	return pm.commitChan
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
