package bft

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	// Import service2's PubSub
	"gossipnode/Pubsub"
	"gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	MaxPrepareProofSize  = 256
	MaxMessageSizeBytes  = 1 << 20
	AllowedTimestampSkew = 30 // seconds
)

// GossipSubMessenger now uses service2's PubSub implementation
type GossipSubMessenger struct {
	ctx         context.Context
	host        host.Host
	pubsub      *Pubsub.StructGossipPubSub
	channelName string

	prepareChan chan *PrepareMessage
	commitChan  chan *CommitMessage

	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	started    bool
}

// NewGossipSubMessenger creates messenger using service2's PubSub
func NewGossipSubMessenger(
	ctx context.Context,
	host host.Host,
	protocolID protocol.ID,
	channelName string,
) (*GossipSubMessenger, error) {

	// Initialize service2's GossipPubSub
	gps, err := Pubsub.NewGossipPubSub(host, protocolID)
	if err != nil {
		return nil, fmt.Errorf("failed to create gossip pubsub: %w", err)
	}

	// Create public channel for BFT consensus
	err = Pubsub.CreateChannel(
		gps.GetGossipPubSub(),
		channelName,
		true, // public channel
		[]peer.ID{},
	)
	if err != nil {
		log.Printf("⚠️  Channel might already exist: %v", err)
	}

	msgCtx, cancel := context.WithCancel(ctx)

	messenger := &GossipSubMessenger{
		ctx:         msgCtx,
		host:        host,
		pubsub:      gps,
		channelName: channelName,
		prepareChan: make(chan *PrepareMessage, 100),
		commitChan:  make(chan *CommitMessage, 100),
		cancelFunc:  cancel,
	}

	// Subscribe to channel with message handler
	err = Pubsub.Subscribe(
		gps.GetGossipPubSub(),
		channelName,
		messenger.handleMessage,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to channel: %w", err)
	}

	return messenger, nil
}

func (g *GossipSubMessenger) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.started {
		return fmt.Errorf("messenger already started")
	}

	g.started = true
	log.Printf("✅ GossipSub messenger started on channel: %s", g.channelName)
	return nil
}

func (g *GossipSubMessenger) Stop() error {
	g.mu.Lock()
	if !g.started {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()

	g.cancelFunc()
	g.wg.Wait()

	// Unsubscribe from channel
	Pubsub.Unsubscribe(g.pubsub.GetGossipPubSub(), g.channelName)

	close(g.prepareChan)
	close(g.commitChan)

	log.Printf("✅ GossipSub messenger stopped")
	return nil
}

// handleMessage processes incoming messages from service2's PubSub
func (g *GossipSubMessenger) handleMessage(msg *PubSubMessages.GossipMessage) {
	// Skip own messages
	if msg.Sender == g.host.ID() {
		return
	}

	if msg.Data == nil {
		log.Printf("⚠️  Message missing data payload")
		return
	}

	payload := []byte(msg.Data.Message)

	// Check message size
	if len(payload) > MaxMessageSizeBytes {
		log.Printf("⚠️  Dropping oversized message from %s", msg.Sender)
		return
	}

	// Get message type from metadata
	msgType, ok := msg.Metadata["type"]
	if !ok {
		log.Printf("⚠️  Message missing type metadata")
		return
	}

	switch msgType {
	case "PREPARE":
		g.handlePrepare(msg)
	case "COMMIT":
		g.handleCommit(msg)
	default:
		log.Printf("⚠️  Unknown message type: %s", msgType)
	}
}

// handlePrepare processes PREPARE messages
func (g *GossipSubMessenger) handlePrepare(msg *PubSubMessages.GossipMessage) {
	var prepareMsg PrepareMessage
	if msg.Data == nil {
		log.Printf("⚠️  PREPARE message missing data from %s", msg.Sender)
		return
	}
	if err := json.Unmarshal([]byte(msg.Data.Message), &prepareMsg); err != nil {
		log.Printf("⚠️  Failed to parse PREPARE message: %v", err)
		return
	}

	if !isTimestampFresh(prepareMsg.Timestamp) {
		log.Printf("⚠️  Stale PREPARE message from %s (ts=%d)", prepareMsg.BuddyID, prepareMsg.Timestamp)
		return
	}

	if err := verifyPrepareSignature(&prepareMsg); err != nil {
		log.Printf("⚠️  Invalid PREPARE signature from %s: %v", prepareMsg.BuddyID, err)
		return
	}

	select {
	case g.prepareChan <- &prepareMsg:
	default:
		log.Printf("⚠️  PREPARE channel full, dropping message from %s", prepareMsg.BuddyID)
	}
}

// handleCommit processes COMMIT messages
func (g *GossipSubMessenger) handleCommit(msg *PubSubMessages.GossipMessage) {
	var commitMsg CommitMessage
	if msg.Data == nil {
		log.Printf("⚠️  COMMIT message missing data from %s", msg.Sender)
		return
	}
	if err := json.Unmarshal([]byte(msg.Data.Message), &commitMsg); err != nil {
		log.Printf("⚠️  Failed to parse COMMIT message: %v", err)
		return
	}

	if len(commitMsg.PrepareProof) > MaxPrepareProofSize {
		log.Printf("⚠️  COMMIT prepare proof too large from %s: %d > %d", commitMsg.BuddyID, len(commitMsg.PrepareProof), MaxPrepareProofSize)
		return
	}

	if !isTimestampFresh(commitMsg.Timestamp) {
		log.Printf("⚠️  Stale COMMIT message from %s (ts=%d)", commitMsg.BuddyID, commitMsg.Timestamp)
		return
	}

	for _, p := range commitMsg.PrepareProof {
		if err := verifyPrepareSignature(&p); err != nil {
			log.Printf("⚠️  Invalid prepare in commit proof from %s: %v", commitMsg.BuddyID, err)
			return
		}
	}

	if err := verifyCommitSignature(&commitMsg); err != nil {
		log.Printf("⚠️  Invalid COMMIT signature from %s: %v", commitMsg.BuddyID, err)
		return
	}

	select {
	case g.commitChan <- &commitMsg:
	default:
		log.Printf("⚠️  COMMIT channel full, dropping message from %s", commitMsg.BuddyID)
	}
}

// BroadcastPrepare sends PREPARE message using service2's PubSub
func (g *GossipSubMessenger) BroadcastPrepare(msg *PrepareMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal PREPARE: %w", err)
	}

	data := &PubSubMessages.Message{
		Message:   string(payload),
		Timestamp: time.Now().Unix(),
	}

	metadata := map[string]string{
		"type":      "PREPARE",
		"buddyID":   msg.BuddyID,
		"decision":  string(msg.Decision),
		"round":     fmt.Sprintf("%d", msg.Round),
		"blockHash": msg.BlockHash,
	}

	err = Pubsub.Publish(
		g.pubsub.GetGossipPubSub(),
		g.channelName,
		data,
		metadata,
	)
	if err != nil {
		return fmt.Errorf("failed to publish PREPARE: %w", err)
	}

	log.Printf("📤 Broadcasted PREPARE from %s (decision: %s)", msg.BuddyID, msg.Decision)
	return nil
}

// BroadcastCommit sends COMMIT message using service2's PubSub
func (g *GossipSubMessenger) BroadcastCommit(msg *CommitMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal COMMIT: %w", err)
	}

	data := &PubSubMessages.Message{
		Message:   string(payload),
		Timestamp: time.Now().Unix(),
	}

	metadata := map[string]string{
		"type":      "COMMIT",
		"buddyID":   msg.BuddyID,
		"decision":  string(msg.Decision),
		"round":     fmt.Sprintf("%d", msg.Round),
		"blockHash": msg.BlockHash,
	}

	err = Pubsub.Publish(
		g.pubsub.GetGossipPubSub(),
		g.channelName,
		data,
		metadata,
	)
	if err != nil {
		return fmt.Errorf("failed to publish COMMIT: %w", err)
	}

	log.Printf("📤 Broadcasted COMMIT from %s (decision: %s)", msg.BuddyID, msg.Decision)
	return nil
}

func (g *GossipSubMessenger) ReceivePrepare() <-chan *PrepareMessage {
	return g.prepareChan
}

func (g *GossipSubMessenger) ReceiveCommit() <-chan *CommitMessage {
	return g.commitChan
}

// GetPeerCount returns number of connected peers (uses service2's function)
func (g *GossipSubMessenger) GetPeerCount() int {
	return Pubsub.GetPeerCount(g.pubsub.GetGossipPubSub())
}
