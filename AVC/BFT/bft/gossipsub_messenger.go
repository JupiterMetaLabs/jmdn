// =============================================================================
// FILE: pkg/bft/gossipsub_messenger.go
// =============================================================================
package bft

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
)

const (
	MaxPrepareProofSize  = 256
	MaxMessageSizeBytes  = 1 << 20
	AllowedTimestampSkew = 30 // seconds
)

type GossipSubMessenger struct {
	ctx       context.Context
	host      host.Host
	ps        *pubsub.PubSub
	topic     *pubsub.Topic
	topicName string

	prepareSub *pubsub.Subscription

	prepareChan chan *PrepareMessage
	commitChan  chan *CommitMessage

	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	started    bool
}

func NewGossipSubMessenger(
	ctx context.Context,
	host host.Host,
	ps *pubsub.PubSub,
	topicName string,
) (*GossipSubMessenger, error) {

	topic, err := ps.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to topic: %w", err)
	}

	msgCtx, cancel := context.WithCancel(ctx)

	messenger := &GossipSubMessenger{
		ctx:         msgCtx,
		host:        host,
		ps:          ps,
		topic:       topic,
		topicName:   topicName,
		prepareSub:  sub,
		prepareChan: make(chan *PrepareMessage, 100),
		commitChan:  make(chan *CommitMessage, 100),
		cancelFunc:  cancel,
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
	g.wg.Add(1)
	go g.listen()
	log.Printf("✅ GossipSub messenger started on topic: %s", g.topicName)
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

	if g.prepareSub != nil {
		g.prepareSub.Cancel()
	}
	if g.topic != nil {
		g.topic.Close()
	}

	close(g.prepareChan)
	close(g.commitChan)

	log.Printf("✅ GossipSub messenger stopped")
	return nil
}

func (g *GossipSubMessenger) listen() {
	defer g.wg.Done()
	log.Printf("🎧 Started listening on GossipSub topic: %s", g.topicName)

	for {
		select {
		case <-g.ctx.Done():
			log.Printf("🛑 Listener stopped (context cancelled)")
			return
		default:
			msg, err := g.prepareSub.Next(g.ctx)
			if err != nil {
				if g.ctx.Err() != nil {
					return
				}
				log.Printf("⚠️  Error receiving message: %v", err)
				continue
			}

			if msg.ReceivedFrom == g.host.ID() {
				continue
			}

			if len(msg.Data) > MaxMessageSizeBytes {
				log.Printf("⚠️ Dropping oversized message from %s (%d bytes)", msg.ReceivedFrom, len(msg.Data))
				continue
			}

			g.handleMessage(msg.Data)
		}
	}
}

type MessageEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func createEnvelope(msgType string, payload interface{}) ([]byte, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	envelope := MessageEnvelope{
		Type:    msgType,
		Payload: payloadBytes,
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal envelope: %w", err)
	}
	return envelopeBytes, nil
}

func (g *GossipSubMessenger) handleMessage(data []byte) {
	var envelope MessageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		log.Printf("⚠️  Failed to parse message envelope: %v", err)
		return
	}

	switch envelope.Type {
	case "PREPARE":
		var prepareMsg PrepareMessage
		if err := json.Unmarshal(envelope.Payload, &prepareMsg); err != nil {
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

	case "COMMIT":
		var commitMsg CommitMessage
		if err := json.Unmarshal(envelope.Payload, &commitMsg); err != nil {
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

	default:
		log.Printf("⚠️  Unknown message type: %s", envelope.Type)
	}
}

func (g *GossipSubMessenger) BroadcastPrepare(msg *PrepareMessage) error {
	envelope, err := createEnvelope("PREPARE", msg)
	if err != nil {
		return fmt.Errorf("failed to create envelope: %w", err)
	}
	if err := g.topic.Publish(g.ctx, envelope); err != nil {
		return fmt.Errorf("failed to publish PREPARE: %w", err)
	}
	log.Printf("📤 Broadcasted PREPARE from %s (decision: %s)", msg.BuddyID, msg.Decision)
	return nil
}

func (g *GossipSubMessenger) BroadcastCommit(msg *CommitMessage) error {
	envelope, err := createEnvelope("COMMIT", msg)
	if err != nil {
		return fmt.Errorf("failed to create envelope: %w", err)
	}
	if err := g.topic.Publish(g.ctx, envelope); err != nil {
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
