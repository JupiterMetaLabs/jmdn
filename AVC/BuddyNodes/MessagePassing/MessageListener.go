package MessagePassing

import (
	"bufio"
	"context"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
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

		// Initialize PubSub node if not already done
		pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
		if pubSubNode == nil {
			fmt.Printf("=== Initializing PubSub_BuddyNode for vote submission ===\n")
			// Get the ForListner (which should be initialized)
			listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
			if listenerNode == nil {
				log.LogMessagesError("ForListner not initialized - cannot process vote", nil, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
				return
			}

			// Create GossipPubSub for this node
			gps := AVCStruct.NewGossipPubSubBuilder(nil).
				SetHost(listenerNode.Host).
				SetProtocol(config.BuddyNodesMessageProtocol).
				Build()

			// Create and set PubSub_BuddyNode
			pubSubBuddyNode := NewBuddyNode(listenerNode.Host, &listenerNode.BuddyNodes, nil, gps)
			AVCStruct.NewGlobalVariables().Set_PubSubNode(pubSubBuddyNode)
			pubSubNode = pubSubBuddyNode
			fmt.Printf("=== PubSub_BuddyNode initialized successfully ===\n")
		}

		// First Add to local CRDT Engine
		if err := Structs.SubmitMessage(message, pubSubNode.PubSub, AVCStruct.NewGlobalVariables().Get_ForListner()); err != nil {
			log.LogMessagesError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
			return
		}
	case config.Type_AskForSubscription:
		// Delegate subscription handling to ListenerHandler
		listenerHandler := NewListenerHandler(StructListenerNode.ResponseHandler)
		listenerHandler.handleAskForSubscription(s, message)
	case config.Type_SubscriptionResponse:
		// Handle subscription response from buddy nodes
		fmt.Printf("=== SEQUENCER: Received subscription response from %s ===\n", s.Conn().RemotePeer())
		fmt.Printf("Message: %s\n", message.Message)
		fmt.Printf("ACK Status: %s\n", message.GetACK().GetStatus())

		log.LogMessagesInfo(fmt.Sprintf("Received subscription response from %s: %s", s.Conn().RemotePeer(), message.Message),
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("function", "ListenMessages.HandleSubmitMessageStream"))

		// Route the response to the ResponseHandler if available
		if StructListenerNode.ResponseHandler != nil {
			accepted := message.GetACK().GetStatus() == "ACK_TRUE"
			fmt.Printf("Routing response to ResponseHandler: %s (accepted: %t)\n", s.Conn().RemotePeer(), accepted)
			StructListenerNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), accepted, "main")
			fmt.Printf("Successfully routed subscription response to ResponseHandler\n")
			log.LogMessagesInfo("Successfully routed subscription response to ResponseHandler",
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("accepted", fmt.Sprintf("%t", accepted)),
				zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		} else {
			fmt.Printf("ERROR: No ResponseHandler set - subscription response not routed\n")
			log.LogMessagesError("No ResponseHandler set - subscription response not routed",
				nil,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		}
	default:
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
	}
}

func (StructListenerNode *StructListener) HandleSubscriptionResponse(s network.Stream, message *AVCStruct.Message, peerID peer.ID) {
	// If message is nil, read from the stream
	if message == nil {
		reader := bufio.NewReader(s)
		responseMsg, err := reader.ReadString(config.Delimiter)
		if err != nil {
			fmt.Printf("Error reading response from stream: %v\n", err)
			return
		}

		fmt.Printf("=== HandleSubscriptionResponse: Received response: %s ===\n", responseMsg)

		message = AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
		if message == nil {
			fmt.Printf("Failed to parse response message\n")
			return
		}
	}

	fmt.Printf("=== HandleSubscriptionResponse: Received response from %s (expecting peer: %s) ===\n", s.Conn().RemotePeer(), peerID)
	fmt.Printf("Message: %s\n", message.Message)
	fmt.Printf("ACK Status: %s\n", message.GetACK().GetStatus())

	log.LogMessagesInfo(fmt.Sprintf("HandleSubscriptionResponse: Received response from %s", s.Conn().RemotePeer()),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("expected_peer", peerID.String()),
		zap.String("topic", log.Messages_TOPIC),
		zap.String("function", "HandleSubscriptionResponse"))

	// Route the response to ResponseHandler if available
	// Use peerID (the peer we sent the request to) not s.Conn().RemotePeer() (the peer responding)
	if StructListenerNode.ResponseHandler != nil {
		accepted := message.GetACK().GetStatus() == "ACK_TRUE"
		fmt.Printf("Routing response to ResponseHandler: %s (accepted: %t)\n", peerID, accepted)

		StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")
		fmt.Printf("Successfully routed subscription response to ResponseHandler\n")

		log.LogMessagesInfo("Successfully routed subscription response to ResponseHandler",
			zap.String("peer", peerID.String()),
			zap.String("accepted", fmt.Sprintf("%t", accepted)),
			zap.String("function", "HandleSubscriptionResponse"))
	} else {
		fmt.Printf("ERROR: No ResponseHandler set - subscription response not routed\n")
		log.LogMessagesError("No ResponseHandler set - subscription response not routed",
			nil,
			zap.String("peer", peerID.String()),
			zap.String("function", "HandleSubscriptionResponse"))
	}
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (StructListenerNode *StructListener) SendMessageToPeer(peerID peer.ID, message string) error {
	fmt.Println("Sending message to peer: ", peerID)
	fmt.Println("Message: ", message)
	fmt.Println("--------------------------------")
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

	// Read response after sending (for subscription requests)
	reader := bufio.NewReader(stream)
	responseMsg, err := reader.ReadString(config.Delimiter)
	if err == nil && responseMsg != "" {
		fmt.Printf("=== SendMessageToPeer: Received response from %s: %s ===\n", peerID, responseMsg)

		// Parse the response message
		responseMessage := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
		if responseMessage != nil && responseMessage.GetACK() != nil {
			// Process the subscription response directly
			if responseMessage.GetACK().GetStage() == config.Type_SubscriptionResponse {
				fmt.Printf("=== SendMessageToPeer: Processing subscription response from %s ===\n", peerID)

				// Route the response to ResponseHandler if available
				if StructListenerNode.ResponseHandler != nil {
					accepted := responseMessage.GetACK().GetStatus() == "ACK_TRUE"
					fmt.Printf("Routing response to ResponseHandler: %s (accepted: %t)\n", peerID, accepted)

					StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")
					fmt.Printf("Successfully routed subscription response to ResponseHandler\n")
				}
			}
		}
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
	defer stream.Close()

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	// Read response after sending (for subscription requests)
	reader := bufio.NewReader(stream)
	responseMsg, err := reader.ReadString(config.Delimiter)
	if err == nil && responseMsg != "" {
		fmt.Printf("=== sendViaSeedNode: Received response from %s: %s ===\n", peerID, responseMsg)

		// Parse the response message
		responseMessage := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
		if responseMessage != nil && responseMessage.GetACK() != nil {
			// Process the subscription response directly
			if responseMessage.GetACK().GetStage() == config.Type_SubscriptionResponse {
				fmt.Printf("=== sendViaSeedNode: Processing subscription response from %s ===\n", peerID)

				// Route the response to ResponseHandler if available
				if StructListenerNode.ResponseHandler != nil {
					accepted := responseMessage.GetACK().GetStatus() == "ACK_TRUE"
					fmt.Printf("Routing response to ResponseHandler: %s (accepted: %t)\n", peerID, accepted)

					StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")
					fmt.Printf("Successfully routed subscription response to ResponseHandler\n")
				}
			}
		}
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
