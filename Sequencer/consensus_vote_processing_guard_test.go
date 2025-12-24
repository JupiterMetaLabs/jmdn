package Sequencer

import (
	"sync"
	"testing"
)

func TestTryStartVoteProcessing_GivenSameBlockAlreadyProcessing_WhenCalled_ThenFalse(t *testing.T) {
	// Given
	consensus := &Consensus{mu: &sync.RWMutex{}}

	started1 := consensus.tryStartVoteProcessing("blockA")
	if !started1 {
		t.Fatalf("expected first tryStartVoteProcessing to succeed")
	}

	// When
	started2 := consensus.tryStartVoteProcessing("blockA")

	// Then
	if started2 {
		t.Fatalf("expected second tryStartVoteProcessing for same block to be rejected")
	}
}

func TestTryStartVoteProcessing_GivenDifferentBlockWhileProcessing_WhenCalled_ThenTrue(t *testing.T) {
	// Given
	consensus := &Consensus{mu: &sync.RWMutex{}}
	_ = consensus.tryStartVoteProcessing("blockA")

	// When
	started := consensus.tryStartVoteProcessing("blockB")

	// Then
	if !started {
		t.Fatalf("expected tryStartVoteProcessing to allow different block")
	}
}


