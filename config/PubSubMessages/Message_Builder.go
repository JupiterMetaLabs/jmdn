package PubSubMessages

import (
	"encoding/json"
	"strings"

	"jmdn/config"

	"github.com/libp2p/go-libp2p/core/peer"
)

func NewGossipMessageBuilder(gossipmessage *GossipMessage) *GossipMessage {
	if gossipmessage != nil {
		return &GossipMessage{
			ID:        gossipmessage.ID,
			Topic:     gossipmessage.Topic,
			Data:      gossipmessage.Data,
			Sender:    gossipmessage.Sender,
			Timestamp: gossipmessage.Timestamp,
			TTL:       gossipmessage.TTL,
			Metadata:  gossipmessage.Metadata,
		}
	}
	return &GossipMessage{}
}

func NewMessageBuilder(message *Message) *Message {
	if message != nil {
		return &Message{
			Sender:    message.Sender,
			Message:   message.Message,
			Timestamp: message.Timestamp,
			ACK:       message.ACK,
		}
	}
	return &Message{}
}

// < -- Gossip Message Builder Pattern -- >
func (gossipmessage *GossipMessage) SetID(id string) *GossipMessage {
	gossipmessage.ID = id
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetID() string {
	return gossipmessage.ID
}

func (gossipmessage *GossipMessage) SetTopic(topic string) *GossipMessage {
	gossipmessage.Topic = topic
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetTopic() string {
	return gossipmessage.Topic
}

func (gossipmessage *GossipMessage) SetMesssage(message *Message) *GossipMessage {
	msg := NewMessageBuilder(message)
	gossipmessage.Data = msg
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetMessage() *Message {
	return NewMessageBuilder(gossipmessage.Data)
}

func (gossipmessage *GossipMessage) SetSender(sender peer.ID) *GossipMessage {
	gossipmessage.Sender = sender
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetSender() peer.ID {
	return gossipmessage.Sender
}

func (gossipmessage *GossipMessage) SetTimestamp(timestamp int64) *GossipMessage {
	gossipmessage.Timestamp = timestamp
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetTimestamp() int64 {
	return gossipmessage.Timestamp
}

func (gossipmessage *GossipMessage) SetTTL(ttl int) *GossipMessage {
	gossipmessage.TTL = ttl
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetTTL() int {
	return gossipmessage.TTL
}

func (gossipmessage *GossipMessage) SetMetadata(metadata map[string]string) *GossipMessage {
	gossipmessage.Metadata = metadata
	return gossipmessage
}

func (gossipmessage *GossipMessage) GetMetadata() map[string]string {
	return gossipmessage.Metadata
}

// < -- Message Builder Pattern -- >
func (msg *Message) SetSender(sender peer.ID) *Message {
	msg.Sender = sender
	return msg
}

func (msg *Message) GetSender() peer.ID {
	return msg.Sender
}

func (msg *Message) SetMessage(message string) *Message {
	msg.Message = message
	return msg
}

func (msg *Message) GetMessage() *string {
	return &msg.Message
}

func (msg *Message) SetTimestamp(timestamp int64) *Message {
	msg.Timestamp = timestamp
	return msg
}

func (msg *Message) GetTimestamp() int64 {
	return msg.Timestamp
}

func (msg *Message) SetACK(ack *ACK) *Message {
	msg.ACK = ack
	return msg
}

func (msg *Message) GetACK() *ACK {
	if msg == nil {
		return nil
	}
	return msg.ACK
}

func (msg *Message) Marshal() ([]byte, error) {
	return json.Marshal(msg)
}

func (msg *Message) ToString() string {
	data, err := msg.Marshal()
	if err != nil {
		return ""
	}
	return string(data)
}

// DeferenceMessage implements the Interface_Message interface
func (msg *Message) DeferenceMessage(message string) *Message {
	message = strings.TrimSuffix(message, string(rune(config.Delimiter)))
	if err := json.Unmarshal([]byte(message), &msg); err != nil {
		return nil
	}
	return msg
}
