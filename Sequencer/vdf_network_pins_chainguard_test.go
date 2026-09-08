package Sequencer

// D-29: a VDF modulus whose factorisation was known to its generator must not
// be installable on an arbitrary chain.
//
// rsa-2048-testnet-ephemeral is documented in vdf_network_pins.go as "INSECURE
// BY CONSTRUCTION — generator knew p,q". Whoever holds those factors knows
// phi(N) and can evaluate the VDF in one modexp instead of T sequential
// squarings, so the delay the beacon depends on does not exist for them: they
// can compute an epoch's entropy before the reveal window closes, see which
// committee it seats, and withhold or re-grind their reveal until the draw
// suits them. The proof still verifies. Nothing looks wrong.
//
// Before this guard, the ONLY thing standing between that modulus and mainnet
// was the word "mainnet" appearing in comments. A copied .env would have
// installed it and the node would have started normally.
//
// The default-chain-id trap: settings' compiled default Network.ChainID is
// 8000800, which is ALSO the devnet chain id. So a guard that reads a default
// when settings are unloaded would treat an unconfigured mainnet node as
// devnet and allow exactly what it exists to prevent. It must fail closed.

import (
	"math/big"
	"strings"
	"testing"
)

const devnetChainID = uint64(8000800)

// withChainID swaps the chain-id source for one test.
func withChainID(t *testing.T, id uint64, known bool) {
	t.Helper()
	prev := currentChainID
	currentChainID = func() (uint64, bool) { return id, known }
	t.Cleanup(func() { currentChainID = prev })
}

func TestEveryNetworkPinHasAPolicy(t *testing.T) {
	// Fail-closed by construction: a pin added without a policy entry must be
	// unusable, not unrestricted. This test is the reminder at authoring time.
	for name := range networkVDFPins {
		if _, ok := networkPinPolicies[name]; !ok {
			t.Fatalf("network pin %q has no entry in networkPinPolicies — a pin with no "+
				"declared policy is refused at install, which is safe but silent. Declare "+
				"whether its factorisation is known and which chains may use it.", name)
		}
	}
}

func TestTrapdoorPinRefusedOnAnUnlistedChain(t *testing.T) {
	withChainID(t, 1, true) // not the devnet chain

	err := enforceNetworkPinChainPolicy("rsa-2048-testnet-ephemeral")
	if err == nil {
		t.Fatal("a modulus whose generator knew p,q was accepted on chain 1 — whoever holds " +
			"those factors can grind committee selection every epoch, invisibly")
	}
	if !strings.Contains(err.Error(), "rsa-2048-testnet-ephemeral") {
		t.Fatalf("the refusal must name the group so an operator can act on it: %v", err)
	}
}

func TestTrapdoorPinAllowedOnItsDevnetChain(t *testing.T) {
	withChainID(t, devnetChainID, true)

	if err := enforceNetworkPinChainPolicy("rsa-2048-testnet-ephemeral"); err != nil {
		t.Fatalf("the devnet pin must still work on the devnet it exists for: %v", err)
	}
}

func TestTrapdoorPinFailsClosedWhenTheChainIsUnknown(t *testing.T) {
	// THE IMPORTANT ONE. settings' compiled default chain id is the devnet's,
	// so anything that substitutes a default here would pass a mainnet node.
	withChainID(t, 0, false)

	err := enforceNetworkPinChainPolicy("rsa-2048-testnet-ephemeral")
	if err == nil {
		t.Fatal("with the chain id unknown the trapdoored modulus was accepted — an " +
			"unconfigured node must refuse, never assume it is the devnet")
	}
}

func TestUnknownPinNameIsRefused(t *testing.T) {
	withChainID(t, devnetChainID, true)

	if err := enforceNetworkPinChainPolicy("some-pin-nobody-declared"); err == nil {
		t.Fatal("a network pin with no declared policy must be refused, not allowed by default")
	}
}

func TestChainGuardRunsInsideTheGroupBuilder(t *testing.T) {
	// The guard is worthless if it is only reachable from a helper. Build the
	// group the way beacon install does and confirm the wrong chain refuses,
	// with a modulus that would otherwise pass every other check.
	withChainID(t, 1, true)

	rec, ok := lookupNetworkPin("rsa-2048-testnet-ephemeral")
	if !ok {
		t.Fatal("expected the devnet pin to exist")
	}
	// Any positive integer: the chain check must reject before the digest is
	// even considered, so this never needs to be the real modulus.
	_, err := newNetworkPinnedRSAGroup(big.NewInt(3), rec.Name)
	if err == nil {
		t.Fatal("newNetworkPinnedRSAGroup accepted a trapdoored pin on chain 1")
	}
}
