package messaging

// D-38(b): avcvotes.AllowUnsignedValidatorVotes was fail-open in production —
// a production node could boot with it enabled, same class of gap as the
// three flags ValidateProductionConsensusPosture already checked.

import (
	"strings"
	"testing"

	"gossipnode/config/settings"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// withPassingPosture sets every OTHER posture input to its safe value and
// restores the originals on cleanup, so a test can flip exactly one input
// and know any resulting error is about that input alone.
func withPassingPosture(t *testing.T) {
	t.Helper()
	prevReject, prevRegistry, prevBody := RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding
	prevUnsigned := avcvotes.AllowUnsignedValidatorVotes
	RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = true, true, true
	avcvotes.AllowUnsignedValidatorVotes = false

	// TestMain (blockPropagation_test.go) already loads settings once for
	// the whole package test run.
	cfg := settings.Get()
	prevPin := cfg.Consensus.SequencerPinnedPeerID
	cfg.Consensus.SequencerPinnedPeerID = "pinned-peer"

	t.Cleanup(func() {
		RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = prevReject, prevRegistry, prevBody
		avcvotes.AllowUnsignedValidatorVotes = prevUnsigned
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
	avcvotes.AllowUnsignedValidatorVotes = true
	defer func() { avcvotes.AllowUnsignedValidatorVotes = false }()
	if err := ValidateProductionConsensusPosture(false); err != nil {
		t.Fatalf("non-production posture must never fail, got: %v", err)
	}
}

func TestValidateProductionConsensusPosture_FlagsUnsignedValidatorVotes(t *testing.T) {
	withPassingPosture(t)
	avcvotes.AllowUnsignedValidatorVotes = true

	err := ValidateProductionConsensusPosture(true)
	if err == nil {
		t.Fatal("a production node with AllowUnsignedValidatorVotes on must refuse to start")
	}
	if !strings.Contains(err.Error(), "AllowUnsignedValidatorVotes") {
		t.Fatalf("error must name AllowUnsignedValidatorVotes so an operator knows which setting to fix, got: %v", err)
	}
}
