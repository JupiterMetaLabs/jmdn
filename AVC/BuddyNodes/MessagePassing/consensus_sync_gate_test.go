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
