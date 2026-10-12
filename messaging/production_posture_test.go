package messaging

import (
	"testing"

	"gossipnode/config/settings"
)

// withPassingPosture sets every OTHER posture input to its safe value and
// restores the originals on cleanup, so a test can flip exactly one input
// and know any resulting error is about that input alone.
func withPassingPosture(t *testing.T) {
	t.Helper()
	prevReject, prevRegistry, prevBody := RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding
	RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = true, true, true

	// TestMain (blockPropagation_test.go) already loads settings once for
	// the whole package test run.
	cfg := settings.Get()
	prevPin := cfg.Consensus.SequencerPinnedPeerID
	cfg.Consensus.SequencerPinnedPeerID = "pinned-peer"

	t.Cleanup(func() {
		RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = prevReject, prevRegistry, prevBody
		cfg.Consensus.SequencerPinnedPeerID = prevPin
	})
}

func TestValidateProductionConsensusPosture_AllPassing(t *testing.T) {
	withPassingPosture(t)
	if err := ValidateProductionConsensusPosture(true); err != nil {
		t.Fatalf("a fully-hardened posture must pass, got: %v", err)
	}
}

func TestValidateProductionConsensusPosture_NonProductionSkipsEntirely(t *testing.T) {
	if err := ValidateProductionConsensusPosture(false); err != nil {
		t.Fatalf("non-production posture must never fail, got: %v", err)
	}
}
