package adapters_test

import (
	"testing"

	avccfg "github.com/JupiterMetaLabs/avc/config"

	"gossipnode/consensus/adapters"
)

// BuildAVCConfig is the assembly point that closes parity divergences #2 (v3),
// #4 (chain id) and #14 (committee size). These tests pin that it produces a
// valid v3 config bound to the given chain id, and — critically — that it
// FAILS CLOSED rather than emitting a config that would silently never verify.

func TestBuildAVCConfig_ProducesValidV3(t *testing.T) {
	c, err := adapters.BuildAVCConfig(7000700, 13, "jmdn-salt", "http://seed:9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Consensus.VoteDomainVersion != avccfg.VoteDomainV3 {
		t.Errorf("VoteDomainVersion = %q, want %q", c.Consensus.VoteDomainVersion, avccfg.VoteDomainV3)
	}
	if c.Network.ChainID != 7000700 {
		t.Errorf("ChainID = %d, want 7000700", c.Network.ChainID)
	}
	if c.Consensus.CommitteeSize != 13 || c.Consensus.MaxMainPeers != 13 {
		t.Errorf("CommitteeSize=%d MaxMainPeers=%d, want both 13 (must agree)",
			c.Consensus.CommitteeSize, c.Consensus.MaxMainPeers)
	}
	if c.Network.NetworkSalt != "jmdn-salt" || c.Network.SeedNode != "http://seed:9000" {
		t.Errorf("salt=%q seed=%q not threaded through", c.Network.NetworkSalt, c.Network.SeedNode)
	}
}

// FAIL-CLOSED: v3 with chain id 0 must be REJECTED — a v3 signature bound to
// chain 0 would silently never verify against real peers. This is the whole
// reason we source chain id from DomainChainID() and never default it to 0.
func TestBuildAVCConfig_RejectsZeroChainID(t *testing.T) {
	if _, err := adapters.BuildAVCConfig(0, 13, "salt", "url"); err == nil {
		t.Fatal("BuildAVCConfig with chainID=0 under v3 must error (fail-closed), got nil")
	}
}

func TestBuildAVCConfig_RejectsNonPositiveCommitteeSize(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := adapters.BuildAVCConfig(7000700, n, "salt", "url"); err == nil {
			t.Errorf("BuildAVCConfig with committeeSize=%d must error, got nil", n)
		}
	}
}
