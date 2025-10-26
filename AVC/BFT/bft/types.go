// =============================================================================
// FILE: pkg/bft/types.go
// =============================================================================
package bft

import "time"

// Decision represents the vote decision
type Decision string

const (
	Accept Decision = "ACCEPT"
	Reject Decision = "REJECT"
)

// BuddyInput represents one buddy's input to BFT
type BuddyInput struct {
	ID         string
	Decision   Decision
	PublicKey  []byte
	PrivateKey []byte // Only for local buddy
}

// PrepareMessage is Phase 3a message (vote broadcast)
type PrepareMessage struct {
	Version   string   `json:"version"` // e.g. "PREPARE_V1"
	Seq       uint64   `json:"seq"`     // monotonic per buddy
	Round     uint64   `json:"round"`
	BlockHash string   `json:"block_hash"`
	BuddyID   string   `json:"buddy_id"`
	Decision  Decision `json:"decision"`
	Timestamp int64    `json:"timestamp"` // UTC unix seconds
	Signature []byte   `json:"signature"` // Ed25519 signature over canonical payload
}

// CommitMessage is Phase 3b message with proof
type CommitMessage struct {
	Version      string           `json:"version"` // e.g. "COMMIT_V1"
	Seq          uint64           `json:"seq"`     // monotonic per buddy
	Round        uint64           `json:"round"`
	BlockHash    string           `json:"block_hash"`
	BuddyID      string           `json:"buddy_id"`
	Decision     Decision         `json:"decision"`
	PrepareProof []PrepareMessage `json:"prepare_proof"`
	Timestamp    int64            `json:"timestamp"`
	Signature    []byte           `json:"signature"`
}

// Result contains the BFT consensus outcome
type Result struct {
	Success       bool
	BlockAccepted bool
	Decision      Decision

	Round           uint64
	BlockHash       string
	TotalDuration   time.Duration
	PrepareDuration time.Duration
	CommitDuration  time.Duration

	TotalBuddies      int
	PrepareCount      int
	CommitCount       int
	ByzantineDetected []string

	PreparePhase PhaseResult
	CommitPhase  PhaseResult

	FailureReason string
}

// PhaseResult contains per-phase statistics
type PhaseResult struct {
	Duration     time.Duration
	AcceptVotes  int
	RejectVotes  int
	ThresholdMet bool
	Threshold    int
	Decision     Decision
}

// Config contains BFT configuration
type Config struct {
	MinBuddies         int
	MaxBuddies         int
	ByzantineTolerance int
	PrepareTimeout     time.Duration
	CommitTimeout      time.Duration
	RequireSignatures  bool
	ValidateProofs     bool
	MaxProofSize       int // cap proof length per commit
}

// DefaultConfig returns production config
func DefaultConfig() Config {
	return Config{
		MinBuddies:         4,
		MaxBuddies:         1000,
		ByzantineTolerance: 0, // Auto-calc
		PrepareTimeout:     8 * time.Second,
		CommitTimeout:      8 * time.Second,
		RequireSignatures:  false,
		ValidateProofs:     true,
		MaxProofSize:       256,
	}
}

// Messenger interface for network communication
type Messenger interface {
	BroadcastPrepare(msg *PrepareMessage) error
	BroadcastCommit(msg *CommitMessage) error
	ReceivePrepare() <-chan *PrepareMessage
	ReceiveCommit() <-chan *CommitMessage
}
