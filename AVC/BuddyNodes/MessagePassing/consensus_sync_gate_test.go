package MessagePassing

import "testing"

// Vote-gate policy: a node may vote only if it holds the latest block or trails
// the sequencer head by at most MaxConsensusLagBlocks (2). A fresh node never
// votes; an unknown head permits (sequencer / seednode outage).
func TestConsensusVoteEligible(t *testing.T) {
	if MaxConsensusLagBlocks != 2 {
		t.Fatalf("policy expects a 2-block lag budget, got %d", MaxConsensusLagBlocks)
	}
	cases := []struct {
		name               string
		localHead, seqHead uint64
		headKnown          bool
		want               bool
	}{
		{"fresh node never votes (head known)", 0, 5, true, false},
		{"fresh node never votes (head unknown)", 0, 0, false, false},
		{"at head votes", 5, 5, true, true},
		{"one behind votes", 4, 5, true, true},
		{"two behind votes (boundary)", 3, 5, true, true},
		{"three behind abstains", 2, 5, true, false},
		{"far behind abstains", 1, 50, true, false},
		{"ahead of reported head votes", 6, 5, true, true},
		{"head unknown permits (seed outage / sequencer)", 5, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConsensusVoteEligible(tc.localHead, tc.seqHead, tc.headKnown); got != tc.want {
				t.Fatalf("ConsensusVoteEligible(local=%d seq=%d known=%v)=%v, want %v",
					tc.localHead, tc.seqHead, tc.headKnown, got, tc.want)
			}
		})
	}
}

// GateDecision fails OPEN on an unknown local tip (a transient read error), and
// defers to ConsensusVoteEligible when the tip is known — so a read hiccup cannot
// stall quorum while a CONFIRMED empty chain / known lag still abstains.
func TestGateDecision_ReadErrorFailsOpen(t *testing.T) {
	// Unknown local tip (localTipKnown=false) → permit, regardless of head/gap.
	if !GateDecision(false, 0, 100, true) {
		t.Fatal("read error (unknown local tip) must fail OPEN (permit), not abstain")
	}
	if !GateDecision(false, 5, 100, true) {
		t.Fatal("read error must permit even against a large known head")
	}
	// Known tip → identical to ConsensusVoteEligible.
	if GateDecision(true, 0, 5, true) {
		t.Fatal("confirmed empty chain (tip 0) must still abstain")
	}
	if GateDecision(true, 2, 5, true) { // gap 3 > MaxConsensusLagBlocks(2)
		t.Fatal("known gap > MaxConsensusLagBlocks must abstain")
	}
	if !GateDecision(true, 3, 5, true) { // gap 2
		t.Fatal("known gap == MaxConsensusLagBlocks must permit")
	}
	if !GateDecision(true, 5, 0, false) { // head unknown, non-empty chain
		t.Fatal("unknown head with a non-empty chain must permit")
	}
}

func TestConsensusVoteReady_GateWiring(t *testing.T) {
	orig := consensusSyncGate
	origEnforce := enforceConsensusSyncGate
	t.Cleanup(func() { consensusSyncGate = orig; enforceConsensusSyncGate = origEnforce })

	// Default-off: even a gate that says "not synced" must NOT block voting, so
	// a buddy that can't self-assess never silently stalls.
	enforceConsensusSyncGate = false
	SetConsensusSyncGate(func() bool { return false })
	if !consensusVoteReady() {
		t.Fatal("gate disabled: voting must be permitted regardless of the wired gate")
	}

	// Enabled: the wired gate decides.
	enforceConsensusSyncGate = true
	consensusSyncGate = nil
	if !consensusVoteReady() {
		t.Fatal("enabled + nil gate must permit voting (sequencer / tests)")
	}
	SetConsensusSyncGate(func() bool { return false })
	if consensusVoteReady() {
		t.Fatal("enabled: a gate returning false must block voting")
	}
	SetConsensusSyncGate(func() bool { return true })
	if !consensusVoteReady() {
		t.Fatal("enabled: a gate returning true must permit voting")
	}
}

// TestConsensusVoteReady_RefusesWhenSlotStoreNotRecovered is the vote-side
// proof for docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8: a node whose
// slot/epoch clock has not been recovered from committed history — the exact
// state a freshly restarted node is in before main.go's
// messaging.RecoverSlotStoreAtStartup call succeeds — must not cast a
// consensus vote, independent of whatever the block-height sync gate above
// says. Simulates "restarted, still at slot 0" via slotStoreReadyFn
// reporting false, since this package cannot import messaging directly (see
// SetSlotStoreReadyFn's doc for the import-cycle reason) — main.go wires the
// real function to messaging.SlotStoreReady in production.
func TestConsensusVoteReady_RefusesWhenSlotStoreNotRecovered(t *testing.T) {
	origFn := slotStoreReadyFn
	origEnforceSlot := enforceSlotRecoveryGate
	origEnforceSync := enforceConsensusSyncGate
	origSyncGate := consensusSyncGate
	t.Cleanup(func() {
		slotStoreReadyFn = origFn
		enforceSlotRecoveryGate = origEnforceSlot
		enforceConsensusSyncGate = origEnforceSync
		consensusSyncGate = origSyncGate
	})

	enforceSlotRecoveryGate = true
	// Make every OTHER gate maximally permissive, so the only thing that
	// could possibly block the vote is the slot-recovery check itself — a
	// test that left enforceConsensusSyncGate on could pass for the wrong
	// reason.
	enforceConsensusSyncGate = false
	SetConsensusSyncGate(nil)

	SetSlotStoreReadyFn(func() bool { return true })
	if !consensusVoteReady() {
		t.Fatal("sanity check failed: expected consensusVoteReady() to permit once SlotStoreReady() reports true, before testing the false case")
	}

	SetSlotStoreReadyFn(func() bool { return false })
	if consensusVoteReady() {
		t.Fatal("a restarted node whose SlotStore has not been recovered (SlotStoreReady()==false) must NOT be permitted to vote, even with every other gate wide open")
	}

	// Recovery completes — voting must be permitted again.
	SetSlotStoreReadyFn(func() bool { return true })
	if !consensusVoteReady() {
		t.Fatal("once SlotStoreReady() reports true, voting must be permitted again")
	}

	// The escape hatch: an operator who explicitly disables the gate (e.g. a
	// harness that never wires recovery at all) must not be blocked by it.
	enforceSlotRecoveryGate = false
	SetSlotStoreReadyFn(func() bool { return false })
	if !consensusVoteReady() {
		t.Fatal("enforceSlotRecoveryGate=false must bypass the slot-recovery check entirely")
	}

	// And the default-nil case (nothing wired at all, e.g. an unrelated
	// existing test in this package) must not newly abstain.
	enforceSlotRecoveryGate = true
	slotStoreReadyFn = nil
	if !consensusVoteReady() {
		t.Fatal("an unwired slotStoreReadyFn (nil) must permit voting, matching consensusSyncGate's own nil convention")
	}
}
