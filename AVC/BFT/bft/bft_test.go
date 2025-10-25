// =============================================================================
// FILE: pkg/bft/bft_test.go
// =============================================================================
package bft

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HAPPY PATH TESTS
// =============================================================================

func TestHappyPath_AllAccept(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, allAccept)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for i, result := range results {
		assert.True(t, result.Success, "Buddy %d failed", i)
		assert.True(t, result.BlockAccepted, "Buddy %d", i)
		assert.Equal(t, Accept, result.Decision, "Buddy %d", i)
		assert.GreaterOrEqual(t, result.CommitCount, 9)
		assert.Zero(t, len(result.ByzantineDetected))
	}

	t.Logf("✓ All 13 buddies reached consensus: ACCEPT")
}

func TestHappyPath_AllReject(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, allReject)

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for _, result := range results {
		assert.True(t, result.Success)
		assert.False(t, result.BlockAccepted)
		assert.Equal(t, Reject, result.Decision)
	}

	t.Logf("✓ All buddies reached consensus: REJECT")
}

func TestHappyPath_9Accept_4Reject(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, func(i int) Decision {
		if i < 9 {
			return Accept
		}
		return Reject
	})

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for _, result := range results {
		assert.True(t, result.Success)
		assert.True(t, result.BlockAccepted)
		assert.Equal(t, Accept, result.Decision)
	}

	t.Logf("✓ 9 ACCEPT vs 4 REJECT - consensus reached")
}

// =============================================================================
// EDGE & FAILURE TESTS
// =============================================================================

func TestSmallNetwork_4Buddies(t *testing.T) {
	config := DefaultConfig()
	config.MinBuddies = 4

	buddies, engines, sim := setupNetworkWithConfig(t, 4, func(i int) Decision {
		if i < 3 {
			return Accept
		}
		return Reject
	}, config)

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for i, result := range results {
		assert.True(t, result.Success, "Buddy %d", i)
		assert.True(t, result.BlockAccepted)
	}

	t.Logf("✓ 4 buddies (minimum) reached consensus")
}

func TestByzantine_ConflictingPrepares(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, func(i int) Decision {
		if i < 9 {
			return Accept
		}
		return Reject
	})

	sim.MarkByzantine("buddy-0")

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	successCount := 0
	byzantineDetected := 0

	for _, result := range results {
		if result.Success {
			successCount++
		}
		if len(result.ByzantineDetected) > 0 {
			byzantineDetected++
		}
	}

	assert.GreaterOrEqual(t, successCount, 9)
	assert.Greater(t, byzantineDetected, 0)

	t.Logf("✓ Byzantine detected and handled")
}

func TestThreshold_ExactlyAtThreshold(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, func(i int) Decision {
		if i < 9 {
			return Accept
		}
		return Reject
	})

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for _, result := range results {
		assert.True(t, result.Success)
		assert.True(t, result.BlockAccepted)
	}

	t.Logf("✓ Threshold exactly met (9/13)")
}

func TestThreshold_BelowThreshold_Timeout(t *testing.T) {
	buddies, engines, sim := setupNetwork(t, 13, func(i int) Decision {
		if i < 8 {
			return Accept
		}
		return Reject
	})

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	for _, result := range results {
		assert.False(t, result.Success)
		assert.Contains(t, result.FailureReason, "timeout")
	}

	t.Logf("✓ Timeout when threshold not met")
}

func TestNetwork_MessageLoss_10Percent(t *testing.T) {
	config := DefaultConfig()
	config.PrepareTimeout = 8 * time.Second
	config.CommitTimeout = 8 * time.Second

	buddies, engines, sim := setupNetworkWithConfig(t, 13, allAccept, config)
	sim.SetDropRate(0.1)

	ctx := context.Background()
	results := runConsensusParallel(t, ctx, buddies, engines, sim, 1, "0xblock1")

	successCount := 0
	for _, result := range results {
		if result.Success {
			successCount++
		}
	}

	assert.GreaterOrEqual(t, successCount, 9)
	t.Logf("✓ Tolerates 10%% message loss")
}

// =============================================================================
// VALIDATION TESTS
// =============================================================================

func TestValidation_InsufficientBuddies(t *testing.T) {
	b := New(DefaultConfig())

	buddies, _ := createBuddies(3, allAccept)

	ctx := context.Background()
	sim := NewSimulator([]string{"buddy-0", "buddy-1", "buddy-2"})

	_, err := b.RunConsensus(ctx, 1, "0xblock1", "buddy-0", buddies, sim.ForBuddy("buddy-0"), NewLocalSigner(buddies[0].PrivateKey))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient buddies")
}

func TestValidation_DuplicateBuddyID(t *testing.T) {
	b := New(DefaultConfig())

	pub, priv, _ := ed25519.GenerateKey(nil)

	buddies := []BuddyInput{
		{ID: "buddy-0", Decision: Accept, PublicKey: pub, PrivateKey: priv},
		{ID: "buddy-0", Decision: Accept, PublicKey: pub},
	}

	ctx := context.Background()
	sim := NewSimulator([]string{"buddy-0"})

	_, err := b.RunConsensus(ctx, 1, "0xblock1", "buddy-0", buddies, sim.ForBuddy("buddy-0"), NewLocalSigner(priv))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

// =============================================================================
// HELPERS
// =============================================================================

func allAccept(i int) Decision {
	return Accept
}

func allReject(i int) Decision {
	return Reject
}

func setupNetwork(t *testing.T, count int, decisionFunc func(int) Decision) (
	[]BuddyInput, []*BFT, *Simulator,
) {
	return setupNetworkWithConfig(t, count, decisionFunc, DefaultConfig())
}

func setupNetworkWithConfig(
	t *testing.T,
	count int,
	decisionFunc func(int) Decision,
	config Config,
) ([]BuddyInput, []*BFT, *Simulator) {

	buddies, buddyIDs := createBuddies(count, decisionFunc)
	sim := NewSimulator(buddyIDs)

	engines := make([]*BFT, count)
	for i := 0; i < count; i++ {
		engines[i] = New(config)
	}

	return buddies, engines, sim
}

func createBuddies(count int, decisionFunc func(int) Decision) ([]BuddyInput, []string) {
	buddies := make([]BuddyInput, count)
	buddyIDs := make([]string, count)

	for i := 0; i < count; i++ {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			panic(err)
		}

		id := fmt.Sprintf("buddy-%d", i)
		buddyIDs[i] = id

		buddies[i] = BuddyInput{
			ID:         id,
			Decision:   decisionFunc(i),
			PublicKey:  pub,
			PrivateKey: priv,
		}
	}

	return buddies, buddyIDs
}

// runConsensusParallel runs consensus concurrently for all buddies.
func runConsensusParallel(
	t *testing.T,
	ctx context.Context,
	buddies []BuddyInput,
	engines []*BFT,
	sim *Simulator,
	round uint64,
	blockHash string,
) []*Result {

	var wg sync.WaitGroup
	results := make([]*Result, len(engines))

	for i := range engines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			signer := NewLocalSigner(buddies[idx].PrivateKey)

			result, _ := engines[idx].RunConsensus(
				ctx,
				round,
				blockHash,
				buddies[idx].ID,
				buddies,
				sim.ForBuddy(buddies[idx].ID),
				signer,
			)
			results[idx] = result
		}(i)
	}

	wg.Wait()
	return results
}
