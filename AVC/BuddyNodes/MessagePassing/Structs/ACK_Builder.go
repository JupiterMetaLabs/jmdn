package Structs

import (
	"encoding/json"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)


type ACK_MESSAGE_REQUEST struct {
	ACK_Message *Pubsub.ACK_Message
}
// < -- ACK Builder Pattern -- >
// Builder constructor
func NewACKBuilder() *ACK_MESSAGE_REQUEST {
	return &ACK_MESSAGE_REQUEST{
		ACK_Message: &Pubsub.ACK_Message{},
	}
}

// Chainable builder methods
func (ack *ACK_MESSAGE_REQUEST) setTrueStatus() *ACK_MESSAGE_REQUEST {
	ack.ACK_Message.Status = config.Type_ACK_True
	return ack
}

func (ack *ACK_MESSAGE_REQUEST) setFalseStatus() *ACK_MESSAGE_REQUEST {
	ack.ACK_Message.Status = config.Type_ACK_False
	return ack
}

func (ack *ACK_MESSAGE_REQUEST) setPeerID(peerID peer.ID) *ACK_MESSAGE_REQUEST {
	ack.ACK_Message.PeerID = peerID.String()
	return ack
}

func (ack *ACK_MESSAGE_REQUEST) setStage(stage string) *ACK_MESSAGE_REQUEST {
	ack.ACK_Message.Stage = stage
	return ack
}

func (ack *ACK_MESSAGE_REQUEST) Marshal() ([]byte, error) {
	return json.Marshal(ack)
} 

func (ack *ACK_MESSAGE_REQUEST) ToString() string {
	data, err := ack.Marshal()
	if err != nil {
		return ""
	}
	return string(data)
}

func (ack *ACK_MESSAGE_REQUEST) GetACK_Message() *Pubsub.ACK_Message {
	return ack.ACK_Message
}

// Trying to acheive the builder pattern for the ACK message
func (ack *ACK_MESSAGE_REQUEST) True_ACK_Message(peerID peer.ID, Stage string) *ACK_MESSAGE_REQUEST {
	return NewACKBuilder().setTrueStatus().setPeerID(peerID).setStage(Stage)
}

func (ack *ACK_MESSAGE_REQUEST) False_ACK_Message(peerID peer.ID, Stage string) *ACK_MESSAGE_REQUEST {
	return NewACKBuilder().setFalseStatus().setPeerID(peerID).setStage(Stage)
}

type PUBSUB_Message struct{
	Message *Pubsub.Message
}

// < -- Message Builder Pattern -- >
func NewMessageBuilder() *PUBSUB_Message {
	return &PUBSUB_Message{
		Message: &Pubsub.Message{},
	}
}

func (msg *PUBSUB_Message) SetSender(sender peer.ID) *PUBSUB_Message {
	msg.Message.Sender = sender
	return msg
}

func (msg *PUBSUB_Message) SetMessage(message string) *PUBSUB_Message {
	msg.Message.Message = message
	return msg
}

func (msg *PUBSUB_Message) SetTimestamp(timestamp int64) *PUBSUB_Message {
	msg.Message.Timestamp = timestamp
	return msg
}

func (msg *PUBSUB_Message) SetACK(ack *Pubsub.ACK_Message) *PUBSUB_Message {
	msg.Message.ACK = ack
	return msg
}

func (msg *PUBSUB_Message) GetMessage() *Pubsub.Message {
	return msg.Message
}

func (msg *PUBSUB_Message) Marshal() ([]byte, error) {
	return json.Marshal(msg)
}

func (msg *PUBSUB_Message) ToString() string {
	data, err := msg.Marshal()
	if err != nil {
		return ""
	}
	return string(data)
}

// MessageProcessor implements Interface_Message
type MessageProcessor struct{}

func NewMessageProcessor() *MessageProcessor {
	return &MessageProcessor{}
}

// DeferenceMessage implements the Interface_Message interface
func (mp *MessageProcessor) DeferenceMessage(msg string) *PUBSUB_Message {
	msg = strings.TrimSuffix(msg, string(rune(config.Delimiter)))
	var message *PUBSUB_Message
	if err := json.Unmarshal([]byte(msg), &message); err != nil {
		return nil
	}
	return message
}


// < -- Singleton Pattern for gloabl variables -- >
type GlobalVariables struct{}

func NewGlobalVariables() *GlobalVariables {
	return &GlobalVariables{}
}

func(globalvar *GlobalVariables) Set_PubSubNode(pubsub *BuddyNode) {
	PubSub_BuddyNode = pubsub
}

func(globalvar *GlobalVariables) Get_PubSubNode() *BuddyNode {
	return PubSub_BuddyNode
}

func(globalvar *GlobalVariables) Set_ForListner(forlistener *BuddyNode) {
	ForListner = forlistener
}

func(globalvar *GlobalVariables) Get_ForListner() *BuddyNode {
	return ForListner
}