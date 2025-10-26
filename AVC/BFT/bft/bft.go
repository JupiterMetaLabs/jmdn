// =============================================================================
// FILE: pkg/bft/bft.go
// =============================================================================
package bft

import (
	"context"
	"fmt"
	"time"
)

// BFT is the main BFT consensus engine
type BFT struct {
	config Config
}

// New creates a new BFT instance
func New(config Config) *BFT {
	if config.ByzantineTolerance == 0 {
		config.ByzantineTolerance = (config.MinBuddies - 1) / 3
	}
	return &BFT{config: config}
}

// RunConsensus executes BFT consensus
// Note: signer may be nil (fallback to raw private key if present)
func (b *BFT) RunConsensus(
	ctx context.Context,
	round uint64,
	blockHash string,
	myBuddyID string,
	allBuddies []BuddyInput,
	messenger Messenger,
	signer Signer,
) (*Result, error) {

	startTime := time.Now()

	// Validate inputs
	if err := b.validateInputs(myBuddyID, allBuddies); err != nil {
		return nil, fmt.Errorf("invalid inputs: %w", err)
	}

	// Find my buddy
	var myBuddy *BuddyInput
	buddyMap := make(map[string]*BuddyInput)
	for i := range allBuddies {
		buddyMap[allBuddies[i].ID] = &allBuddies[i]
		if allBuddies[i].ID == myBuddyID {
			myBuddy = &allBuddies[i]
		}
	}

	if myBuddy == nil {
		return nil, fmt.Errorf("my buddy ID not found")
	}

	threshold := b.calculateThreshold(len(allBuddies))

	engine := &engine{
		config:      b.config,
		myBuddyID:   myBuddyID,
		myDecision:  myBuddy.Decision,
		buddies:     buddyMap,
		threshold:   threshold,
		round:       round,
		blockHash:   blockHash,
		byzantine:   newByzantineDetector(),
		prepareMsgs: make(map[string]*PrepareMessage),
		commitMsgs:  make(map[string]*CommitMessage),
		lastSeqSeen: make(map[string]uint64),
	}

	// Phase 3a: PREPARE
	prepareStart := time.Now()
	prepareResult, err := engine.runPrepare(ctx, messenger)
	prepareDuration := time.Since(prepareStart)

	if err != nil {
		return &Result{
			Success:         false,
			BlockAccepted:   false,
			Decision:        Reject,
			Round:           round,
			BlockHash:       blockHash,
			TotalDuration:   time.Since(startTime),
			PrepareDuration: prepareDuration,
			TotalBuddies:    len(allBuddies),
			FailureReason:   fmt.Sprintf("PREPARE failed: %v", err),
		}, err
	}

	// Phase 3b: COMMIT
	commitStart := time.Now()
	commitResult, err := engine.runCommit(ctx, messenger, prepareResult.Decision)
	commitDuration := time.Since(commitStart)

	if err != nil {
		return &Result{
			Success:         false,
			BlockAccepted:   false,
			Decision:        Reject,
			Round:           round,
			BlockHash:       blockHash,
			TotalDuration:   time.Since(startTime),
			PrepareDuration: prepareDuration,
			CommitDuration:  commitDuration,
			TotalBuddies:    len(allBuddies),
			PreparePhase:    *prepareResult,
			FailureReason:   fmt.Sprintf("COMMIT failed: %v", err),
		}, err
	}

	return &Result{
		Success:           true,
		BlockAccepted:     commitResult.Decision == Accept,
		Decision:          commitResult.Decision,
		Round:             round,
		BlockHash:         blockHash,
		TotalDuration:     time.Since(startTime),
		PrepareDuration:   prepareDuration,
		CommitDuration:    commitDuration,
		TotalBuddies:      len(allBuddies),
		PrepareCount:      len(engine.prepareMsgs),
		CommitCount:       len(engine.commitMsgs),
		ByzantineDetected: engine.byzantine.getAll(),
		PreparePhase:      *prepareResult,
		CommitPhase:       *commitResult,
	}, nil
}

func (b *BFT) validateInputs(myBuddyID string, buddies []BuddyInput) error {
	if myBuddyID == "" {
		return fmt.Errorf("empty buddy ID")
	}
	if len(buddies) < b.config.MinBuddies {
		return fmt.Errorf("insufficient buddies: have %d, need %d", len(buddies), b.config.MinBuddies)
	}

	seen := make(map[string]bool)
	for i, buddy := range buddies {
		if buddy.ID == "" {
			return fmt.Errorf("buddy %d has empty ID", i)
		}
		if seen[buddy.ID] {
			return fmt.Errorf("duplicate buddy ID: %s", buddy.ID)
		}
		seen[buddy.ID] = true

		if buddy.Decision != Accept && buddy.Decision != Reject {
			return fmt.Errorf("invalid decision: %s", buddy.Decision)
		}

		if len(buddy.PublicKey) == 0 {
			return fmt.Errorf("buddy %s missing public key", buddy.ID)
		}

		if buddy.ID == myBuddyID && len(buddy.PrivateKey) == 0 {
			return fmt.Errorf("local buddy missing private key")
		}
	}

	return nil
}

func (b *BFT) calculateThreshold(buddyCount int) int {
	f := (buddyCount - 1) / 3
	if b.config.ByzantineTolerance > 0 {
		f = b.config.ByzantineTolerance
	}
	threshold := 2*f + 1
	if threshold > buddyCount {
		threshold = (buddyCount / 2) + 1
	}
	return threshold
}

func (b *BFT) CalculateThreshold(buddyCount int) int {
	return b.calculateThreshold(buddyCount)
}

func (b *BFT) CanTolerate(buddyCount int) int {
	return (buddyCount - 1) / 3
}
