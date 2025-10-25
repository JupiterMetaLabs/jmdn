package types

import (
	"crypto/ed25519"
	"time"
)

// Node represents a network node
type Node struct {
	ID                string
	PublicKey         ed25519.PublicKey
	Address           string
	ASN               string
	Region            string
	ReputationScore   float64
	LastSeen          time.Time
	SelectionScore    int
	LastSelectedRound uint64
	IsActive          bool
	Capacity          int // Derive capacity from weights
}
