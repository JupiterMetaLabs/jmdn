package MessagePassing

import (
	"bufio"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/Service"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)

type StructListener struct{
	ListenerBuddyNode *AVCStruct.BuddyNode	
}

func NewListenerStruct(listner *AVCStruct.BuddyNode) *StructListener{
	return &StructListener{
		ListenerBuddyNode: listner,
	}
}

func (StructListenerNode *StructListener) HandleSubmitMessageStream(s network.Stream) {
	defer s.Close()
	// Add the buddy Node to the Listener node for singleton instance
	AVCStruct.NewGlobalVariables().Set_ForListner(StructListenerNode.ListenerBuddyNode)

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Error reading message from %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		fmt.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	message := AVCStruct.NewMessageProcessor().DeferenceMessage(msg)

	log.LogMessagesInfo(fmt.Sprintf("Received submit message from %s: %s", s.Conn().RemotePeer(), msg), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))

	switch message.Message.Message {
	case config.Type_SubmitVote:
		log.LogMessagesInfo(fmt.Sprintf("Received submit vote from %s: %s", s.Conn().RemotePeer(), message.Message), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
		// First Add to local CRDT Engine
		if err := Service.SubmitMessage(message.GetMessage(), AVCStruct.NewGlobalVariables().Get_PubSubNode().PubSub, AVCStruct.NewGlobalVariables().Get_ForListner()); err != nil {
			log.LogMessagesError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
			return
		}
	default:
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Messages_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleSubmitMessageStream"))
	}
}