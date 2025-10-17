package MessagePassing

import (
	"encoding/json"
	"gossipnode/config"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

type ACK interface {
	True_ACK_Message(PeerID peer.ID, Stage string) *ACK_Message
	False_ACK_Message(PeerID peer.ID, Stage string) *ACK_Message
}

type Interface_Message interface {
	DeferenceMessage(msg string) *Message
}

type ACK_Message struct {
	Status string `json:"status"`
	PeerID string `json:"peer_id"`
	Stage  string `json:"stage"`
}

// < -- ACK Builder Pattern -- >
// Builder constructor
func NewACKBuilder() *ACK_Message {
	return &ACK_Message{}
}

// Chainable builder methods
func (a *ACK_Message) setTrueStatus() *ACK_Message {
	a.Status = config.Type_ACK_True
	return a
}

func (a *ACK_Message) setFalseStatus() *ACK_Message {
	a.Status = config.Type_ACK_False
	return a
}

func (a *ACK_Message) setPeerID(peerID peer.ID) *ACK_Message {
	a.PeerID = peerID.String()
	return a
}

func (a *ACK_Message) setStage(stage string) *ACK_Message {
	a.Stage = stage
	return a
}

func (a *ACK_Message) Marshal() ([]byte, error) {
	return json.Marshal(a)
} 

func (a *ACK_Message) ToString() string {
	data, err := a.Marshal()
	if err != nil {
		return ""
	}
	return string(data)
}

// Trying to acheive the builder pattern for the ACK message
func (ack *ACK_Message) True_ACK_Message(peerID peer.ID, Stage string) *ACK_Message {
	return NewACKBuilder().setTrueStatus().setPeerID(peerID).setStage(Stage)
}

func (ack *ACK_Message) False_ACK_Message(peerID peer.ID, Stage string) *ACK_Message {
	return NewACKBuilder().setFalseStatus().setPeerID(peerID).setStage(Stage)
}

// < -- Message Builder Pattern -- >
func NewMessageBuilder() *Message {
	return &Message{}
}

func (msg *Message) SetSender(sender peer.ID) *Message {
	msg.Sender = sender
	return msg
}

func (msg *Message) SetMessage(message string) *Message {
	msg.Message = message
	return msg
}

func (msg *Message) SetTimestamp(timestamp int64) *Message {
	msg.Timestamp = timestamp
	return msg
}

func (msg *Message) SetACK(ack *ACK_Message) *Message {
	msg.ACK = ack
	return msg
}

// MessageProcessor implements Interface_Message
type MessageProcessor struct{}

func NewMessageProcessor() *MessageProcessor {
	return &MessageProcessor{}
}

// DeferenceMessage implements the Interface_Message interface
func (mp *MessageProcessor) DeferenceMessage(msg string) *Message {
	msg = strings.TrimSuffix(msg, string(rune(config.Delimiter)))
	var message *Message
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