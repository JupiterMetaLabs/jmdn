package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	Subscription "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

type StructListener struct {
	ListenerBuddyNode *AVCStruct.BuddyNode
	ResponseHandler   AVCStruct.ResponseHandler
}

func NewListenerStruct(listner *AVCStruct.BuddyNode) *StructListener {
	return &StructListener{
		ListenerBuddyNode: listner,
		ResponseHandler:   nil, // Will be set by NewListenerNode
	}
}

func (StructListenerNode *StructListener) HandleSubmitMessageStream(s network.Stream) {
	// Note: Stream closure is handled by the caller to allow response reading

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Error reading message from %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		fmt.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	message := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(msg)

	log.LogMessagesInfo(fmt.Sprintf("Received submit message from %s: %s", s.Conn().RemotePeer(), msg), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))

	// Check if message was successfully parsed
	if message == nil {
		log.LogMessagesError("Failed to parse message - malformed JSON or invalid structure", nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		return
	}

	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		log.LogMessagesError("Received message with nil ACK", nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		return
	}

	switch message.GetACK().GetStage() {
	case config.Type_SubmitVote:
		log.LogMessagesInfo(fmt.Sprintf("Received submit vote from %s: %s", s.Conn().RemotePeer(), message.Message), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		// First Add to local CRDT Engine
		if err := Structs.SubmitMessage(message, AVCStruct.NewGlobalVariables().Get_PubSubNode().PubSub, AVCStruct.NewGlobalVariables().Get_ForListner()); err != nil {
			log.LogMessagesError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
			return
		}
	case config.Type_AskForSubscription:

		// Otherwise, this is a subscription request - handle it
		// Handle subscription request
		log.LogMessagesInfo(fmt.Sprintf("Received subscription request from %s", s.Conn().RemotePeer()),
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("function", "ListenMessages.HandleSubmitMessageStream"))

		// Create GossipPubSub using Pubsub_Builder.go first
		gps := AVCStruct.NewGossipPubSubBuilder(nil).
			SetHost(AVCStruct.NewGlobalVariables().Get_ForListner().Host).
			SetProtocol(config.BuddyNodesMessageProtocol).
			Build()

		// Initialize PubSub BuddyNode if not already done
		if AVCStruct.NewGlobalVariables().Get_PubSubNode() == nil {
			listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
			if listenerNode != nil && listenerNode.Host != nil {
				// Create default Buddies instance
				defaultBuddies := AVCStruct.NewBuddiesBuilder(nil)
				buddy := NewBuddyNode(listenerNode.Host, defaultBuddies, nil, gps)
				AVCStruct.NewGlobalVariables().Set_PubSubNode(buddy)
			}
		}

		// Subscribe to BuddyNodesMessageProtocol
		if err := Subscription.Subscribe(gps, log.Consensus_TOPIC, func(msg *AVCStruct.GossipMessage) {
			log.LogMessagesInfo(fmt.Sprintf("Received message on BuddyNodesMessageProtocol: %s", msg.ID), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		}); err != nil {
			log.LogMessagesError(fmt.Sprintf("Failed to subscribe to BuddyNodesMessageProtocol: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
			sendSubscriptionResponse(s, false)
			return
		}
		sendSubscriptionResponse(s, true)
	default:
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
	}
}

// sendSubscriptionResponse sends ACK response for subscription requests
func sendSubscriptionResponse(s network.Stream, accepted bool) {
	host := s.Conn().LocalPeer()
	var ackBuilder *AVCStruct.ACK
	if accepted {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(host, config.Type_AskForSubscription)
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(host, config.Type_AskForSubscription)
	}

	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(host).
		SetMessage(fmt.Sprintf("Subscription %s", map[bool]string{true: "accepted", false: "rejected"}[accepted])).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackBuilder)

	messageBytes, err := json.Marshal(message)
	if err != nil {
		log.LogMessagesError(fmt.Sprintf("Failed to marshal response: %v", err), err)
		return
	}

	// Send response back through the SAME stream (SubmitMessageProtocol)
	_, err = s.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		log.LogMessagesError(fmt.Sprintf("Failed to send response: %v", err), err)
	} else {
		log.LogMessagesInfo(fmt.Sprintf("Sent subscription response: %s", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]))
	}
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (StructListenerNode *StructListener) SendMessageToPeer(peerID peer.ID, message string) error {
	// Get or create a stream from the cache using SubmitMessageProtocol
	StreamCache := NewStreamCacheBuilder(StructListenerNode.ListenerBuddyNode.StreamCache)
	if StreamCache == nil {
		return fmt.Errorf("failed to get the StreamCache, nil StreamCache occurred")
	}

	stream, err := StreamCache.GetSubmitMessageStream(peerID)
	if err != nil {
		// Direct connection failed, try fallback via seed node
		log.LogConsensusInfo(fmt.Sprintf("Direct connection failed, using seed node fallback for %s", peerID), zap.String("peer", peerID.String()), zap.String("function", "SendMessageToPeer"))
		return StructListenerNode.sendViaSeedNode(peerID, message)
	}

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		// If write fails, the stream might be invalid, close it and try fallback
		StreamCache.CloseStream(peerID)
		log.LogConsensusInfo(fmt.Sprintf("Stream write failed, using seed node fallback for %s", peerID), zap.String("peer", peerID.String()), zap.String("function", "SendMessageToPeer"))
		return StructListenerNode.sendViaSeedNode(peerID, message)
	}

	// Update metadata
	StructListenerNode.ListenerBuddyNode.Mutex.Lock()
	StructListenerNode.ListenerBuddyNode.MetaData.Sent++
	StructListenerNode.ListenerBuddyNode.MetaData.Total++
	StructListenerNode.ListenerBuddyNode.MetaData.UpdatedAt = time.Now()
	StructListenerNode.ListenerBuddyNode.Mutex.Unlock()

	log.LogConsensusInfo(fmt.Sprintf("Sent listener message to %s: %s", peerID, message), zap.String("peer", peerID.String()), zap.String("message", message), zap.String("function", "SendMessageToPeer"))

	return nil
}

// sendViaSeedNode establishes a quick connection via seed node, sends message, and drops connection
func (StructListenerNode *StructListener) sendViaSeedNode(peerID peer.ID, message string) error {
	// Get peer's multiaddr from seed node
	peerInfo, err := StructListenerNode.getPeerInfoFromSeedNode(peerID)
	if err != nil {
		return fmt.Errorf("failed to get peer info from seed node: %v", err)
	}

	// Connect directly to the peer
	if err := StructListenerNode.ListenerBuddyNode.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("failed to connect to peer %s: %v", peerID, err)
	}

	// Open stream and send message using SubmitMessageProtocol
	stream, err := StructListenerNode.ListenerBuddyNode.Host.NewStream(context.Background(), peerID, config.SubmitMessageProtocol)
	if err != nil {
		return fmt.Errorf("failed to create stream to %s: %v", peerID, err)
	}
	defer stream.Close() // Drop connection immediately after sending

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	// Update metadata
	StructListenerNode.ListenerBuddyNode.Mutex.Lock()
	StructListenerNode.ListenerBuddyNode.MetaData.Sent++
	StructListenerNode.ListenerBuddyNode.MetaData.Total++
	StructListenerNode.ListenerBuddyNode.MetaData.UpdatedAt = time.Now()
	StructListenerNode.ListenerBuddyNode.Mutex.Unlock()

	log.LogConsensusInfo(fmt.Sprintf("Sent listener message to %s (via seed node): %s", peerID, message), zap.String("peer", peerID.String()), zap.String("message", message), zap.String("function", "sendViaSeedNode"))

	return nil
}

// getPeerInfoFromSeedNode retrieves peer information from seed node
func (StructListenerNode *StructListener) getPeerInfoFromSeedNode(peerID peer.ID) (*peer.AddrInfo, error) {
	// Create seed node client - configurable via environment variable or use default
	seedNodeURL := os.Getenv("SEED_NODE_URL")
	if seedNodeURL == "" {
		seedNodeURL = config.SeedNodeURL
	}

	client, err := seednode.NewClient(seedNodeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed node client: %v", err)
	}
	defer client.Close()

	// Get peer record from seed node
	peerRecord, err := client.GetPeer(peerID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get peer from seed node: %v", err)
	}

	// Convert multiaddrs to peer.AddrInfo
	peerInfo := &peer.AddrInfo{ID: peerID}
	for _, multiaddrStr := range peerRecord.Multiaddrs {
		addr, err := multiaddr.NewMultiaddr(multiaddrStr)
		if err != nil {
			log.LogConsensusInfo(fmt.Sprintf("Skipping invalid multiaddr: %s", multiaddrStr), zap.String("function", "getPeerInfoFromSeedNode"))
			continue
		}
		peerInfo.Addrs = append(peerInfo.Addrs, addr)
	}

	if len(peerInfo.Addrs) == 0 {
		return nil, fmt.Errorf("no valid multiaddrs found for peer %s", peerID)
	}

	return peerInfo, nil
}
