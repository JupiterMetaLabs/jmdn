package MessagePassing

// Consensus vote sync-gate (P7).
//
// An unsynced node MUST NOT participate in consensus. A node with no local
// chain, or one still catching up, cannot have authenticated the block it would
// be voting on — so it abstains rather than signing a vote over state it has not
// verified. This complements the ingestion-side fail-closed linkage: linkage
// stops an unsynced node from ACCEPTING an unverified chain; this stops it from
// VOTING on one.

// consensusSyncGate, when set, reports whether this node is synced enough to
// cast a consensus vote. It is wired once at node startup (SetConsensusSyncGate).
// When nil the node is permitted to vote — so the sequencer (authoritative, no
// catch-up) and unit tests are unaffected unless they wire a gate.
var consensusSyncGate func() bool

// SetConsensusSyncGate wires the consensus vote sync-gate. Call once at startup.
func SetConsensusSyncGate(fn func() bool) { consensusSyncGate = fn }

// consensusVoteReady reports whether this node may cast a consensus vote now.
func consensusVoteReady() bool {
	if consensusSyncGate == nil {
		return true
	}
	return consensusSyncGate()
}

// ConsensusVoteEligible is the pure policy the production gate applies:
//   - a node with no local chain (tip 0) NEVER votes — a fresh node must not
//     participate in consensus at all;
//   - a node that runs a sync monitor must be reported synced;
//   - otherwise (e.g. the monitor-less sequencer with a non-empty chain) it may
//     vote.
//
// Exposed for the startup wiring and for unit tests.
func ConsensusVoteEligible(localTip uint64, monitorPresent, monitorSynced bool) bool {
	if localTip == 0 {
		return false
	}
	if monitorPresent && !monitorSynced {
		return false
	}
	return true
}
