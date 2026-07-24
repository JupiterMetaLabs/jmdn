package MessagePassing

import "testing"

// Vote-gate policy: fresh node never votes; a monitored node must be synced.
func TestConsensusVoteEligible(t *testing.T) {
	cases := []struct {
		name            string
		tip             uint64
		present, synced bool
		want            bool
	}{
		{"fresh node never votes (no monitor)", 0, false, false, false},
		{"fresh node never votes (even if monitor synced)", 0, true, true, false},
		{"catching up abstains", 5, true, false, false},
		{"synced monitored node votes", 5, true, true, true},
		{"monitorless non-empty node votes (sequencer)", 5, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConsensusVoteEligible(tc.tip, tc.present, tc.synced); got != tc.want {
				t.Fatalf("ConsensusVoteEligible(%d,%v,%v)=%v, want %v",
					tc.tip, tc.present, tc.synced, got, tc.want)
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
