package messaging

import (
	"testing"

	"gossipnode/config/settings"
)

// consensus.p2p toggles direct (p2p) block propagation on top of gossip:
// 0 = gossip-only (default), >=1 also enables direct fan-out. The env override
// (JMDN_DIRECT_BLOCK_PROPAGATION=1) is evaluated at init and not exercised here.
func TestDirectBlockPropagation_ConfigToggle(t *testing.T) {
	if !settings.IsLoaded() {
		t.Skip("settings not loaded")
	}
	c := settings.Get()
	prev := c.Consensus.P2P
	t.Cleanup(func() { c.Consensus.P2P = prev })

	c.Consensus.P2P = 0
	if got := directBlockPropagationEnabled(); got != directBlockPropagationEnv {
		t.Fatalf("consensus.p2p=0 must be gossip-only (defer to env=%v), got %v", directBlockPropagationEnv, got)
	}

	c.Consensus.P2P = 1
	if !directBlockPropagationEnabled() {
		t.Fatal("consensus.p2p=1 must also enable direct (p2p) propagation")
	}
}
