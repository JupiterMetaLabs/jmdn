package MessagePassing

import "os"

// MaxConsensusLagBlocks is how far behind the sequencer head a node may be and
// still participate in consensus. A node must hold the latest block or be at most
// this many blocks behind (head, head-1, head-2 when =2); a node 3+ behind — or
// with no chain at all — must not be a buddy or cast a vote. Policy set with Doc:
// "nodes without the latest block or latest-2 must not be a buddy node or
// participate in consensus."
const MaxConsensusLagBlocks uint64 = 2

// enforceConsensusSyncGate gates the consensus sync-gate. Default-ON: a node that
// is behind by more than MaxConsensusLagBlocks (a KNOWN gap) abstains, so a stale
// node cannot vote on state it has not caught up to. It fails OPEN only when the
// head is genuinely unknown (the sequencer, which runs no monitor, and a seednode
// outage) so a transient loss of the head reference does not stall consensus. Set
// JMDN_ENFORCE_SYNC_GATE=0 to disable (revert to always-permit) during rollout.
var enforceConsensusSyncGate = os.Getenv("JMDN_ENFORCE_SYNC_GATE") != "0"

// Consensus vote sync-gate.
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
	// Default-off: when the gate is not explicitly enforced, always permit
	// voting so a buddy that cannot self-assess sync state does not silently
	// abstain and stall consensus.
	if !enforceConsensusSyncGate {
		return true
	}
	if consensusSyncGate == nil {
		return true
	}
	return consensusSyncGate()
}

// ConsensusVoteEligible is the pure policy the production gate applies, keyed on
// how far this node's local head trails the sequencer head:
//   - a node with no local chain (localHead 0) NEVER votes — a fresh node must
//     not participate in consensus at all;
//   - when the sequencer head is unknown (headKnown=false: the monitor-less
//     sequencer, or a seednode outage) a node with a non-empty chain MAY vote —
//     there is no reference to judge against, so fail open for liveness;
//   - a node at or ahead of the reported head MAY vote;
//   - otherwise the node may vote only if it trails by at most
//     MaxConsensusLagBlocks; a larger, KNOWN gap abstains.
//
// Exposed for the startup wiring and for unit tests.
func ConsensusVoteEligible(localHead, sequencerHead uint64, headKnown bool) bool {
	if localHead == 0 {
		return false
	}
	if !headKnown {
		return true
	}
	if sequencerHead <= localHead {
		return true
	}
	return sequencerHead-localHead <= MaxConsensusLagBlocks
}

// GateDecision is the full vote-gate policy, testable independent of the DB and
// the sync monitor. localTipKnown=false means this node could NOT read its own
// local tip — a transient DB read error, NOT a confirmed empty chain. In that
// case it PERMITS (fails open), consistent with an unknown sequencer head: a read
// hiccup must never pull a validator out of consensus and stall quorum (that was
// the July-halt trigger, now that the gate is default-ON). When the tip is known,
// ConsensusVoteEligible applies — so a CONFIRMED empty chain (localHead 0) still
// abstains, as does a known gap > MaxConsensusLagBlocks.
func GateDecision(localTipKnown bool, localHead, sequencerHead uint64, headKnown bool) bool {
	if !localTipKnown {
		return true
	}
	return ConsensusVoteEligible(localHead, sequencerHead, headKnown)
}
