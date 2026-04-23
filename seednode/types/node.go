package types

import (
	"crypto/ed25519"
	"time"
)

// Node represents a network node
type Node struct {
	PeerId       string    `json:"peerId"`
	Alias        string    `json:"alias"`
	Region       string    `json:"region"`
	ASN          int       `json:"asn"`
	IPPrefix     string    `json:"ipPrefix"`
	Reachability string    `json:"reachability"`
	RTTBucket    string    `json:"rttBucket"`
	RTTMs        int       `json:"rttMs"`
	LastSeen     time.Time `json:"lastSeen"`
	Multiaddrs   []string  `json:"multiaddrs"`
	// Legacy fields for backward compatibility
	ID                string
	PublicKey         ed25519.PublicKey
	Address           string
	ReputationScore   float64
	SelectionScore    float64
	LastSelectedRound uint64
	IsActive          bool
	Capacity          int
}
