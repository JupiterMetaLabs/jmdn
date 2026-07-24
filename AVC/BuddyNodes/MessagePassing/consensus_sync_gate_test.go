package MessagePassing

import "testing"

// P7 vote-gate policy: fresh node never votes; a monitored node must be synced.
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
	t.Cleanup(func() { consensusSyncGate = orig })

	consensusSyncGate = nil
	if !consensusVoteReady() {
		t.Fatal("nil gate must permit voting (sequencer / tests)")
	}
	SetConsensusSyncGate(func() bool { return false })
	if consensusVoteReady() {
		t.Fatal("a gate returning false must block voting")
	}
	SetConsensusSyncGate(func() bool { return true })
	if !consensusVoteReady() {
		t.Fatal("a gate returning true must permit voting")
	}
}
