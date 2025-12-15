// =============================================================================
// FILE 2: pkg/bft/constants.go - ADD THIS
// =============================================================================
package bft

import (
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

const (
	PrepareVersionV1 = "PREPARE_V1"
	CommitVersionV1  = "COMMIT_V1"
)

const (
	AllowedTimestampSkew = 30 // seconds
	MaxPrepareProofSize  = 256
)

const (
	DefaultPrepareTimeout = 10 * time.Second
	DefaultCommitTimeout  = 10 * time.Second
)

// Quorum calculation constants (for 2/3 quorum rule)
const (
	QuorumNumerator   = 2 // Numerator for 2/3 quorum
	QuorumDenominator = 3 // Denominator for 2/3 quorum
)

// BFT threshold calculation constants
const (
	ByzantineFactor  = 3 // Used in (n-1)/3 calculation for Byzantine tolerance
	QuorumMultiplier = 2 // Used in 2f+1 calculation
	QuorumOffset     = 1 // Used in 2f+1 calculation
)

// Committee and buddy node constants
const (
	// DefaultCommitteeSize is the production size for buddy node committees
	// This equals 13 buddies for BFT consensus (threshold = 2f+1 = 9 votes)
	DefaultCommitteeSize = 13
)

var(
	BFTLocal interfaces.LocalGoroutineManagerInterface
)