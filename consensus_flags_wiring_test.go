package main

import (
	"testing"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/config/settings"
	"gossipnode/messaging"
)

// Precedence: a SET legacy env var keeps the package's init-time value (with
// its original parser); an UNSET one takes the YAML value.
func TestApplyConsensusFlags_EnvWinsOverYAMLOnlyWhenSet(t *testing.T) {
	cfg := settings.DefaultConfig()
	cfg.Consensus.Flags.CommitteeV2 = true // yaml says on
	cfg.Consensus.Flags.AggCert = true     // yaml says on
	cfg.Consensus.Flags.EnforceSyncGate = false
	cfg.Consensus.Flags.EnforceSlotRecoveryGate = false
	cfg.Consensus.Flags.VoteCRDTCompactK = 9

	// Simulate what package init produced under the legacy parsers for the
	// env vars we mark as SET: AGG_CERT=true is OFF (exactly-1), and we leave
	// COMMITTEE_V2 unset so YAML applies.
	t.Setenv("JMDN_AVC_AGG_CERT", "true")
	messaging.AggCertEnabled = false
	messaging.CommitteeV2Enabled = false
	saveK := voteCRDTCompactK
	t.Cleanup(func() { voteCRDTCompactK = saveK })

	applyConsensusFlags(&cfg)

	if !messaging.CommitteeV2Enabled {
		t.Fatal("env unset: YAML committee_v2=true must apply")
	}
	if messaging.AggCertEnabled {
		t.Fatal("env SET (JMDN_AVC_AGG_CERT=true → off under exactly-1): YAML must NOT override")
	}
	if messaging.EnforceSlotRecoveryGate {
		t.Fatal("env unset: YAML enforce_slot_recovery_gate=false must apply to messaging")
	}
	if MessagePassing.EnforceSlotRecoveryGateForTest() {
		t.Fatal("env unset: YAML enforce_slot_recovery_gate=false must apply to the MessagePassing reader too")
	}
	if MessagePassing.EnforceConsensusSyncGateForTest() {
		t.Fatal("env unset: YAML enforce_sync_gate=false must apply")
	}
	if voteCRDTCompactK != 9 {
		t.Fatalf("env unset: YAML vote_crdt_compact_k=9 must apply, got %d", voteCRDTCompactK)
	}
}

func TestApplyConsensusFlags_CompactKEnvKeepsRawParser(t *testing.T) {
	cfg := settings.DefaultConfig()
	cfg.Consensus.Flags.VoteCRDTCompactK = 9
	t.Setenv("JMDN_VOTE_CRDT_COMPACT_K", "5")
	saveK := voteCRDTCompactK
	t.Cleanup(func() { voteCRDTCompactK = saveK })
	voteCRDTCompactK = 5 // what init produced
	applyConsensusFlags(&cfg)
	if voteCRDTCompactK != 5 {
		t.Fatalf("env SET must win over YAML for compact_k, got %d", voteCRDTCompactK)
	}
}
