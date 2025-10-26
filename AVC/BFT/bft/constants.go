// =============================================================================
// FILE 2: pkg/bft/constants.go - ADD THIS
// =============================================================================
package bft

import "time"

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
