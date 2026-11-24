package messaging

import (
	"testing"

	"gossipnode/config/settings"
)

// Under v2 a pool member beyond the alphabetical max_validators cap must be
// authorized (it can be seated); with v2 off the legacy capped behaviour holds.
func TestKeyAuthorized_V2UsesUncappedPool(t *testing.T) {
	if !settings.IsLoaded() {
		t.Skip("settings not loaded: committeeSizeLimit() is 0, capped == uncapped")
	}
	a := mustMintMember("peerA", 0x31)
	b := mustMintMember("peerB", 0x32)
	c := mustMintMember("peerC", 0x33)
	d := mustMintMember("peerD", 0x34)
	useEligibleBound(t, a, b, c, d)

	cfg := settings.Get()
	prevCap := cfg.Consensus.MaxValidators
	cfg.Consensus.MaxValidators = 2 // capped set = peerA, peerB
	t.Cleanup(func() { cfg.Consensus.MaxValidators = prevCap })
	prev := CommitteeV2Enabled
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	CommitteeV2Enabled = false
	if keyAuthorized("peerD", d.pubHex) {
		t.Fatal("v2 off: peerD is outside the capped set and must be denied")
	}
	if !keyAuthorized("peerA", a.pubHex) {
		t.Fatal("v2 off: peerA is in the capped set and must be authorized")
	}

	CommitteeV2Enabled = true
	if !keyAuthorized("peerD", d.pubHex) {
		t.Fatal("v2 on: peerD is in the pool and must be authorized")
	}
	if keyAuthorized("peerD", c.pubHex) {
		t.Fatal("v2 on: a pool peer ID with another member's key must still be denied (pubkey binding)")
	}
	if keyAuthorized("peerZ", d.pubHex) {
		t.Fatal("v2 on: a peer outside the pool must be denied")
	}
}
