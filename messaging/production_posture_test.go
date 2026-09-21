package messaging

import "testing"

func TestValidateProductionConsensusPosture_FailClosed(t *testing.T) {
	prevReject := RejectLegacyVotes
	prevCommittee := EnforceCommitteeRegistry
	prevBody := EnforceBodyBinding
	defer func() {
		RejectLegacyVotes = prevReject
		EnforceCommitteeRegistry = prevCommittee
		EnforceBodyBinding = prevBody
	}()

	RejectLegacyVotes = true
	EnforceCommitteeRegistry = true
	EnforceBodyBinding = true
	if err := ValidateProductionConsensusPosture(true); err != nil {
		t.Fatalf("all-on production posture should pass: %v", err)
	}
	if err := ValidateProductionConsensusPosture(false); err != nil {
		t.Fatalf("non-production should always pass: %v", err)
	}

	RejectLegacyVotes = false
	if err := ValidateProductionConsensusPosture(true); err == nil {
		t.Fatal("expected fatal error when RejectLegacyVotes off in production")
	}
	RejectLegacyVotes = true

	EnforceCommitteeRegistry = false
	if err := ValidateProductionConsensusPosture(true); err == nil {
		t.Fatal("expected fatal error when EnforceCommitteeRegistry off in production")
	}
}

func TestConsensusHardeningDefaults_On(t *testing.T) {
	// Defaults must keep fail-open paths OFF (flags ON). Env can override at
	// process start; this asserts the package defaults used when env is unset.
	if !RejectLegacyVotes {
		t.Fatal("RejectLegacyVotes default must be ON")
	}
	if !EnforceCommitteeRegistry {
		t.Fatal("EnforceCommitteeRegistry default must be ON")
	}
	if !EnforceBodyBinding {
		t.Fatal("EnforceBodyBinding default must be ON")
	}
}
