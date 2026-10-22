package CRDTSync

import (
	"encoding/json"
	"time"

	crdt "github.com/JupiterMetaLabs/avc/crdt"
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
