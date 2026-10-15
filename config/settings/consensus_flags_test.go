package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// The YAML defaults equal the historical env defaults, except committee_v2 and
// agg_cert which are ON (testnet fleet value, required ON together).
func TestConsensusFlags_DefaultsMatchLegacyEnvDefaults(t *testing.T) {
	f := DefaultConfig().Consensus.Flags
	want := map[string]bool{
		"committee_snapshot_anchor": false, "committee_v2": true, "reject_legacy_votes": true,
		"enforce_committee_registry": true, "enforce_block_linkage": true, "enforce_body_binding": true,
		"agg_cert":                   true,
		"enforce_slot_recovery_gate": true, "slot_recovery_preslot_migration": false, "enforce_sync_gate": true,
		"emit_block_bound_votes": true, "validator_voter_set": true, "validator_vote_ingest": true,
		"emit_committee_bls": true, "allow_new_receiver_accounts": true, "block_gossip": true,
	}
	got := map[string]bool{
		"committee_snapshot_anchor": f.CommitteeSnapshotAnchor, "committee_v2": f.CommitteeV2, "reject_legacy_votes": f.RejectLegacyVotes,
		"enforce_committee_registry": f.EnforceCommitteeRegistry, "enforce_block_linkage": f.EnforceBlockLinkage, "enforce_body_binding": f.EnforceBodyBinding,
		"agg_cert":                   f.AggCert,
		"enforce_slot_recovery_gate": f.EnforceSlotRecoveryGate, "slot_recovery_preslot_migration": f.SlotRecoveryPreslotMigration, "enforce_sync_gate": f.EnforceSyncGate,
		"emit_block_bound_votes": f.EmitBlockBoundVotes, "validator_voter_set": f.ValidatorVoterSet, "validator_vote_ingest": f.ValidatorVoteIngest,
		"emit_committee_bls": f.EmitCommitteeBLS, "allow_new_receiver_accounts": f.AllowNewReceiverAccounts, "block_gossip": f.BlockGossip,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("flags.%s default = %v, want %v (legacy env default)", k, got[k], w)
		}
	}
	if f.VoteCRDTCompactK != 3 {
		t.Errorf("flags.vote_crdt_compact_k default = %d, want 3", f.VoteCRDTCompactK)
	}
	r := DefaultConfig().Reputation
	if !r.Observe || r.PushIntervalSeconds != 300 || !r.PushSuspend || r.StorePath != "" {
		t.Errorf("reputation defaults = %+v, want observe=true interval=300 suspend=true store=\"\"", r)
	}
}

// A jmdn.yaml that sets the new keys must come through Load(); keys not set
// in the file must keep their defaults (viper merges per key).
func TestConsensusFlags_LoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := `
consensus:
  flags:
    committee_v2: false
    agg_cert: false
    enforce_sync_gate: false
    vote_crdt_compact_k: 7
reputation:
  observe: false
  push_interval_seconds: 0
  store_path: /var/lib/jmdn/reputation.json
`
	if err := os.WriteFile(filepath.Join(dir, "jmdn.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := cfg.Consensus.Flags
	if f.CommitteeV2 || f.AggCert || f.EnforceSyncGate || f.VoteCRDTCompactK != 7 {
		t.Fatalf("yaml overrides not applied: %+v", f)
	}
	// untouched keys keep defaults
	if !f.RejectLegacyVotes || !f.EnforceBodyBinding {
		t.Fatalf("unset keys lost their defaults: %+v", f)
	}
	if cfg.Reputation.Observe || cfg.Reputation.PushIntervalSeconds != 0 || cfg.Reputation.StorePath != "/var/lib/jmdn/reputation.json" || !cfg.Reputation.PushSuspend {
		t.Fatalf("reputation yaml not applied: %+v", cfg.Reputation)
	}
}

// The viper-derived env names work too (JMDN_CONSENSUS_FLAGS_<KEY>), in
// addition to the legacy names handled by applyConsensusFlags.
func TestConsensusFlags_ViperEnvAlias(t *testing.T) {
	t.Setenv("JMDN_CONSENSUS_FLAGS_COMMITTEE_V2", "false")
	t.Setenv("JMDN_REPUTATION_PUSH_INTERVAL_SECONDS", "") // not a viper key; must not interfere
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Consensus.Flags.CommitteeV2 {
		t.Fatal("JMDN_CONSENSUS_FLAGS_COMMITTEE_V2=false not honoured by viper")
	}
}
