package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/Security"
	"gossipnode/config/settings"
	"gossipnode/internal/reputation"
	"gossipnode/messaging"
	"gossipnode/seednode"
)

// applyConsensusFlags gives the consensus/committee feature flags a YAML home
// (consensus.flags.*, reputation.*) without changing what any existing env
// file does.
//
// Precedence per flag: legacy env var SET → the package's own init-time read
// (original parser, untouched) stands; env var UNSET → the YAML/default value
// from settings is written into the package variable. So "env > yaml >
// default", and the three historical parsers (lenient / exactly-1 / not-0)
// keep their exact semantics for anyone still driving flags by env.
//
// Runs once, right after settings.Load and before any consensus component or
// the production-posture check reads these variables. Logs the effective
// value and source of every flag so an incident log answers "which gates was
// this node running with" in one line.
func applyConsensusFlags(cfg *settings.NodeConfig) {
	f := cfg.Consensus.Flags
	type boolFlag struct {
		env  string
		yaml bool
		get  func() bool
		set  func(bool)
	}
	bools := []boolFlag{
		{"JMDN_COMMITTEE_SNAPSHOT_ANCHOR", f.CommitteeSnapshotAnchor, func() bool { return messaging.CommitteeSnapshotAnchorEnabled }, func(b bool) { messaging.CommitteeSnapshotAnchorEnabled = b }},
		{"JMDN_COMMITTEE_V2", f.CommitteeV2, func() bool { return messaging.CommitteeV2Enabled }, func(b bool) { messaging.CommitteeV2Enabled = b }},
		{"JMDN_REJECT_LEGACY_VOTES", f.RejectLegacyVotes, func() bool { return messaging.RejectLegacyVotes }, func(b bool) { messaging.RejectLegacyVotes = b }},
		{"JMDN_ENFORCE_COMMITTEE_REGISTRY", f.EnforceCommitteeRegistry, func() bool { return messaging.EnforceCommitteeRegistry }, func(b bool) { messaging.EnforceCommitteeRegistry = b }},
		{"JMDN_ENFORCE_BLOCK_LINKAGE", f.EnforceBlockLinkage, func() bool { return messaging.EnforceBlockLinkage }, func(b bool) { messaging.EnforceBlockLinkage = b }},
		{"JMDN_ENFORCE_BODY_BINDING", f.EnforceBodyBinding, func() bool { return messaging.EnforceBodyBinding }, func(b bool) { messaging.EnforceBodyBinding = b }},
		{"JMDN_AVC_AGG_CERT", f.AggCert, func() bool { return messaging.AggCertEnabled }, func(b bool) { messaging.AggCertEnabled = b }},
		{"JMDN_TIMEOUT_CERT_WIRING", f.TimeoutCertWiring, func() bool { return messaging.TimeoutCertWiringEnabled }, func(b bool) { messaging.TimeoutCertWiringEnabled = b }},
		{"JMDN_TIMEOUT_CERT_REJOIN", f.TimeoutCertRejoin, func() bool { return messaging.TimeoutCertRejoinEnabled }, func(b bool) { messaging.TimeoutCertRejoinEnabled = b }},
		// One env, two readers (messaging + MessagePassing): keep them equal.
		{"JMDN_ENFORCE_SLOT_RECOVERY_GATE", f.EnforceSlotRecoveryGate, func() bool { return messaging.EnforceSlotRecoveryGate }, func(b bool) {
			messaging.EnforceSlotRecoveryGate = b
			MessagePassing.SetEnforceSlotRecoveryGate(b)
		}},
		{"JMDN_SLOT_RECOVERY_PRESLOT_MIGRATION", f.SlotRecoveryPreslotMigration, func() bool { return messaging.PreSlotMigration }, func(b bool) { messaging.PreSlotMigration = b }},
		{"JMDN_ENFORCE_SYNC_GATE", f.EnforceSyncGate, nil, MessagePassing.SetEnforceConsensusSyncGate},
		{"JMDN_EMIT_BLOCK_BOUND_VOTES", f.EmitBlockBoundVotes, func() bool { return BLS_Signer.EmitBlockBoundVotes }, func(b bool) { BLS_Signer.EmitBlockBoundVotes = b }},
		{"JMDN_VALIDATOR_VOTER_SET", f.ValidatorVoterSet, func() bool { return Structs.ValidatorVoterSetEnabled }, func(b bool) { Structs.ValidatorVoterSetEnabled = b }},
		{"JMDN_VALIDATOR_VOTE_INGEST", f.ValidatorVoteIngest, func() bool { return Structs.ValidatorVoteIngestEnabled }, func(b bool) { Structs.ValidatorVoteIngestEnabled = b }},
		{"JMDN_EMIT_COMMITTEE_BLS", f.EmitCommitteeBLS, func() bool { return seednode.EmitCommitteeBLS }, func(b bool) { seednode.EmitCommitteeBLS = b }},
		{"JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", f.AllowNewReceiverAccounts, func() bool { return Security.AllowNewReceiverAccounts }, func(b bool) { Security.AllowNewReceiverAccounts = b }},
		{"JMDN_BLOCK_GOSSIP", f.BlockGossip, func() bool { return messaging.EnableBlockGossip }, func(b bool) { messaging.EnableBlockGossip = b }},
		{"JMDN_TRUST_SEQUENCER_CERT", f.TrustSequencerCertificate, func() bool { return messaging.TrustSequencerCertificate }, func(b bool) { messaging.TrustSequencerCertificate = b }},
		{"JMDN_FINALIZE_CERT_ONLY", f.FinalizeCertOnly, func() bool { return messaging.FinalizeCertOnly }, func(b bool) { messaging.FinalizeCertOnly = b }},
		{"JMDN_REPUTATION_OBSERVE", cfg.Reputation.Observe, func() bool { return reputation.Enabled }, func(b bool) { reputation.Enabled = b }},
	}

	var lines []string
	for _, bf := range bools {
		var val bool
		src := "yaml"
		if _, set := os.LookupEnv(bf.env); set {
			src = "env"
			if bf.get != nil {
				val = bf.get()
			} else {
				val = os.Getenv(bf.env) != "0" // the one unexported not-0 reader
			}
		} else {
			val = bf.yaml
			bf.set(val)
		}
		lines = append(lines, fmt.Sprintf("%s=%t(%s)", strings.TrimPrefix(bf.env, "JMDN_"), val, src))
	}

	// VoteCRDTCompactK: env (raw uint, existing parser) > yaml > default; the
	// floor-at-2 clamp stays in vote_crdt_compaction.go.
	kSrc := "env"
	if _, set := os.LookupEnv("JMDN_VOTE_CRDT_COMPACT_K"); !set {
		kSrc = "yaml"
		voteCRDTCompactK = clampCompactK(f.VoteCRDTCompactK)
	}
	lines = append(lines, fmt.Sprintf("VOTE_CRDT_COMPACT_K=%d(%s)", voteCRDTCompactK, kSrc))

	sort.Strings(lines)
	fmt.Printf("consensus flags (env > yaml > default): %s\n", strings.Join(lines, " "))
}
