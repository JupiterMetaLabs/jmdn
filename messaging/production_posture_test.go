package messaging

import (
	"strings"
	"testing"

	"gossipnode/config/settings"
)

// withPassingPosture sets every OTHER posture input to its safe value and
// restores the originals on cleanup, so a test can flip exactly one input
// and know any resulting error is about that input alone.
func withPassingPosture(t *testing.T) {
	t.Helper()
	prevReject, prevRegistry, prevBody := RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding
	prevTrust, prevV2 := TrustSequencerCertificate, CommitteeV2Enabled
	RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = true, true, true
	// TrustSequencerCertificate compiles in ON (testnet) and is refused in
	// production; committee-v2 off keeps the anchoring check out of the way
	// unless a test turns it on.
	TrustSequencerCertificate, CommitteeV2Enabled = false, false

	// TestMain (blockPropagation_test.go) already loads settings once for
	// the whole package test run.
	cfg := settings.Get()
	prevPin := cfg.Consensus.SequencerPinnedPeerID
	cfg.Consensus.SequencerPinnedPeerID = "pinned-peer"
	prevCertFrom := cfg.Consensus.SyncCertificateRequiredFrom
	cfg.Consensus.SyncCertificateRequiredFrom = 1

	t.Cleanup(func() {
		RejectLegacyVotes, EnforceCommitteeRegistry, EnforceBodyBinding = prevReject, prevRegistry, prevBody
		TrustSequencerCertificate, CommitteeV2Enabled = prevTrust, prevV2
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

// withAnchorSettings sets the two values committee anchoring is derived from
// and restores them on cleanup. activation 0 means anchoring is off.
func withAnchorSettings(t *testing.T, activation uint64, epochBlocks int64) {
	t.Helper()
	cfg := settings.Get()
	prevAct, prevL := cfg.Consensus.CommitteeAnchorActivationHeight, cfg.Consensus.CommitteeEpochBlocks
	cfg.Consensus.CommitteeAnchorActivationHeight = activation
	cfg.Consensus.CommitteeEpochBlocks = epochBlocks
	t.Cleanup(func() {
		cfg.Consensus.CommitteeAnchorActivationHeight = prevAct
		cfg.Consensus.CommitteeEpochBlocks = prevL
	})
}

// Committee-v2 without an anchor means a synced block's committee cannot be
// proven. Production must refuse to boot, and say which setting to change.
func TestValidateProductionConsensusPosture_RefusesCommitteeV2WithoutAnchor(t *testing.T) {
	withPassingPosture(t)
	CommitteeV2Enabled = true
	withAnchorSettings(t, 0, 20)

	err := ValidateProductionConsensusPosture(true)
	if err == nil {
		t.Fatal("production with committee-v2 on and anchoring off must refuse to start")
	}
	if !strings.Contains(err.Error(), "committee anchoring") || !strings.Contains(err.Error(), "committee_anchor_activation_height") {
		t.Fatalf("error must name the anchoring setting, got: %v", err)
	}
}

func TestValidateProductionConsensusPosture_CommitteeV2WithAnchorPasses(t *testing.T) {
	withPassingPosture(t)
	CommitteeV2Enabled = true
	withAnchorSettings(t, 20, 20)
	if err := ValidateProductionConsensusPosture(true); err != nil {
		t.Fatalf("committee-v2 with anchoring active must pass, got: %v", err)
	}
}

// Anchoring is derived from the activation height AND the epoch length; an
// activation height with a zero epoch length is still "off".
func TestValidateProductionConsensusPosture_ZeroEpochLengthCountsAsNoAnchor(t *testing.T) {
	withPassingPosture(t)
	CommitteeV2Enabled = true
	withAnchorSettings(t, 20, 0)
	if err := ValidateProductionConsensusPosture(true); err == nil {
		t.Fatal("a zero epoch length leaves anchoring off; production must refuse")
	}
}

// Committee-v2 off keeps the legacy single-verifier path: no anchor needed.
func TestValidateProductionConsensusPosture_V2OffDoesNotRequireAnchor(t *testing.T) {
	withPassingPosture(t)
	CommitteeV2Enabled = false
	withAnchorSettings(t, 0, 20)
	if err := ValidateProductionConsensusPosture(true); err != nil {
		t.Fatalf("committee-v2 off must not demand an anchor, got: %v", err)
	}
}

// The compiled-in testnet default for the sequencer-trust stopgap must be refused in
// production, so a production node cannot inherit it by omission.
func TestValidateProductionConsensusPosture_RefusesSequencerTrustStopgap(t *testing.T) {
	withPassingPosture(t)
	TrustSequencerCertificate = true
	err := ValidateProductionConsensusPosture(true)
	if err == nil || !strings.Contains(err.Error(), "TrustSequencerCertificate") {
		t.Fatalf("production must refuse the sequencer-trust stopgap, got: %v", err)
	}
}
