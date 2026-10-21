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
	prevCertFrom := cfg.Consensus.SyncCertificateRequiredFrom
	cfg.Consensus.SyncCertificateRequiredFrom = 1

	t.Cleanup(func() {
		RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = prevReject, prevRegistry, prevBody
		cfg.Consensus.SequencerPinnedPeerID = prevPin
		cfg.Consensus.SyncCertificateRequiredFrom = prevCertFrom
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

// TestValidateProductionConsensusPosture_FlagsUnsignedValidatorVotes was
// removed here: 6226a97 (#193) deleted the capability it exercised
// (avcvotes.AllowUnsignedValidatorVotes) and the posture check that guarded
// it, and dropped this file's avcvotes/strings imports — but left the test
// body behind, so the messaging package's tests could not compile. There is
// no longer a flag to flip, so the test has nothing left to assert.

func TestValidateProductionConsensusPosture_RequiresSyncCertificateHeight(t *testing.T) {
	withPassingPosture(t)
	cfg := settings.Get()
	cfg.Consensus.SyncCertificateRequiredFrom = 0
	if err := ValidateProductionConsensusPosture(true); err == nil {
		t.Fatal("production posture must require consensus.sync_certificate_required_from")
	}
}
