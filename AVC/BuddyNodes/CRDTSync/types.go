package CRDTSync

import (
	"encoding/json"
	"time"

	"jmdn/crdt"
)

// Message represents a message sent over PubSub for CRDT synchronization
type Message struct {
	Type      string                     `json:"type"` // "operation", "sync", or "sync_request"
	NodeID    string                     `json:"node_id"`
	Key       string                     `json:"key"`
	Operation *Operation                 `json:"operation,omitempty"`
	SyncData  map[string]json.RawMessage `json:"sync_data,omitempty"`
	Timestamp time.Time                  `json:"timestamp"`
}

// Operation represents a CRDT operation
type Operation struct {
	Kind    string           `json:"kind"` // "add", "remove", "increment"
	Element string           `json:"element,omitempty"`
	Value   uint64           `json:"value,omitempty"`
	TS      crdt.VectorClock `json:"timestamp"`
}

// MessageStore holds received CRDT messages
type MessageStore struct {
	messages map[string]Message
}

// NewMessageStore creates a new message store
func NewMessageStore() *MessageStore {
	return &MessageStore{
		messages: make(map[string]Message),
	}
}

// Add adds a message to the store
func (ms *MessageStore) Add(key string, msg Message) {
	ms.messages[key] = msg
}

// Get retrieves a message by key
func (ms *MessageStore) Get(key string) (Message, bool) {
	msg, exists := ms.messages[key]
	return msg, exists
}

// GetAll returns all messages
func (ms *MessageStore) GetAll() map[string]Message {
	return ms.messages
}

// Count returns the number of messages
func (ms *MessageStore) Count() int {
	return len(ms.messages)
}

// Clear removes all messages
func (ms *MessageStore) Clear() {
	ms.messages = make(map[string]Message)
}
