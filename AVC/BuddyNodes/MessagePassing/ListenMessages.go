package MessagePassing

import (
	"bufio"
	"context"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Log"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"gossipnode/logging"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Function to initialize the loggers
func Init_Loggers(loki bool) {
	_, err := log.NewLoggerBuilder().
		SetURL(logging.GetLokiURL(), loki).
		SetFileName("buddy_nodes.log").
		SetTopic(log.Consensus_TOPIC).
		SetDirectory("logs").
		SetBatchSize(100).
		SetBatchWait(2 * time.Second).
		SetTimeout(6 * time.Second).
		SetKeepLogs(true).
		Build()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize Consensus loggers: %v", err))
	}

	_, err = log.NewLoggerBuilder().
		SetURL(logging.GetLokiURL(), loki).
		SetFileName("buddy_nodes.log").
		SetTopic(log.Messages_TOPIC).
		SetDirectory("logs").
		SetBatchSize(100).
		SetBatchWait(2 * time.Second).
		SetTimeout(6 * time.Second).
		SetKeepLogs(true).
		Build()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize Messages loggers: %v", err))
	}
}

func StartStreamHandlers(h host.Host, buddies *Buddies, responseHandler ResponseHandler, pubsub *Pubsub.GossipPubSub) {
	buddy := NewBuddyNode(h, buddies, responseHandler, pubsub)
	listener := NewListenerNode(h, responseHandler)

	// Set stream handlers (these will run continuously)
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, func(stream network.Stream) {
		log.LogConsensusInfo("New buddy nodes connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.BuddyNodesMessageProtocol)))
		go buddy.HandleBuddyNodesMessageStream(stream)
	})

	h.SetStreamHandler(config.SubmitMessageProtocol, func(stream network.Stream) {
		log.LogMessagesInfo("New submit message connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.SubmitMessageProtocol)))
		go listener.HandleSubmitMessageStream(stream)
	})

	log.LogConsensusInfo("Stream handlers started and listening for connections")
}


func (Listener *BuddyNode) HandleSubmitMessageStream(s network.Stream) {
	defer s.Close()
	// Add the buddy Node to the Listener node for singleton instance
	NewGlobalVariables().Set_ForListner(Listener)

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Error reading message from %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		fmt.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	message := NewMessageProcessor().DeferenceMessage(msg)

	log.LogMessagesInfo(fmt.Sprintf("Received submit message from %s: %s", s.Conn().RemotePeer(), msg), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))

	switch message.Message {
	case config.Type_SubmitVote:
		log.LogMessagesInfo(fmt.Sprintf("Received submit vote from %s: %s", s.Conn().RemotePeer(), message.Message), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		// First Add to local CRDT Engine
		if err := SubmitMessage(message.Message, PubSub_BuddyNode.PubSub, ForListner); err != nil {
			log.LogMessagesError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		}
	default:
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
	}

}

func NewListenerNode(h host.Host, responseHandler ResponseHandler) *BuddyNode {
	listener := &BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		ResponseHandler: responseHandler,
		MetaData: MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now(),
		},
	}

	// Set up the stream handler for the listener nodes message protocol
	h.SetStreamHandler(config.SubmitMessageProtocol, listener.HandleSubmitMessageStream)

	log.LogConsensusInfo(fmt.Sprintf("ListenerNode initialized with ID: %s", h.ID()), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))
	log.LogConsensusInfo(fmt.Sprintf("Listening for listener messages on protocol: %s", config.SubmitMessageProtocol), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))

	return listener
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
func (buddy *BuddyNode) SendMessageToPeer(peerID peer.ID, message string) error {
	// Create a stream to the peer
	stream, err := buddy.Host.NewStream(context.Background(), peerID, config.BuddyNodesMessageProtocol)
	if err != nil {
		return fmt.Errorf("failed to create stream to %s: %v", peerID, err)
	}
	defer stream.Close()

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		buddy.Mutex.Lock()
		buddy.MetaData.Sent++
		buddy.MetaData.Total++
		buddy.MetaData.UpdatedAt = time.Now()
		buddy.Mutex.Unlock()

		log.LogConsensusInfo(fmt.Sprintf("Sent buddy message to %s: %s", peerID, message), zap.String("peer", peerID.String()), zap.String("message", message), zap.String("function", "SendMessageToPeer"))
	}

	return nil
}
