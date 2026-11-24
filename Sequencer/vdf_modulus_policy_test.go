package Sequencer

// D-35 — the D-29 chain guard must key on the MODULUS, not the group name.
//
// THE BYPASS THESE TESTS PIN. enforceNetworkPinChainPolicy is reached only from
// newNetworkPinnedRSAGroup, which buildVDFGroup calls only inside
// `if rec, known := lookupNetworkPin(groupName); known`. That branch keys on the
// NAME. So supplying a restricted modulus under any other name made the lookup
// miss, skipped the chain guard entirely, and fell through to the
// JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS path, which installed it on any chain.
//
// rsa-2048-frc was the ideal cover: a real avc registry entry whose Digest is
// EMPTY (avc/vdf/provenance.go), so NewPinnedRSAGroup refuses it and execution
// reaches the override — and whose published dimensions (2048 bits, 617 digits)
// the devnet modulus matches exactly, so the shape check passes too.
//
// These tests use a synthetic pin rather than the real devnet modulus, because
// the point is the KEYING, not any particular number: if the guard keys on the
// value, a synthetic restricted modulus is refused under a foreign name, and if
// it keys on the name, it is not.

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// withTestNetworkPin registers a restricted pin for the duration of one test.
// Restores both tables, so it cannot leak into another test in this package.
func withTestNetworkPin(t *testing.T, name string, n *big.Int, allowedChain uint64) {
	t.Helper()

	digest, err := vdf.ModulusDigest(n)
	if err != nil {
		t.Fatalf("ModulusDigest: %v", err)
	}

	prevPin, hadPin := networkVDFPins[name]
	prevPol, hadPol := networkPinPolicies[name]

	networkVDFPins[name] = vdf.ProvenanceRecord{
		Name:   name,
		Bits:   n.BitLen(),
		Digits: len(n.String()),
		Digest: digest,
		Source: "SYNTHETIC test pin",
		Note:   "test only",
	}
	networkPinPolicies[name] = networkPinPolicy{
		TrapdoorKnown:   true,
		AllowedChainIDs: []uint64{allowedChain},
	}

	t.Cleanup(func() {
		if hadPin {
			networkVDFPins[name] = prevPin
		} else {
			delete(networkVDFPins, name)
		}
		if hadPol {
			networkPinPolicies[name] = prevPol
		} else {
			delete(networkPinPolicies, name)
		}
	})
}

// withChainIDFor overrides the chain-id seam for one test.
func withChainIDFor(t *testing.T, id uint64, known bool) {
	t.Helper()
	prev := currentChainID
	currentChainID = func() (uint64, bool) { return id, known }
	t.Cleanup(func() { currentChainID = prev })
}

// testModulus is a valid RSA-shaped modulus for policy tests. It never
// evaluates a VDF here — only its digest and shape are exercised.
func testModulus(t *testing.T) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(
		"c1b2a3948576f0e1d2c3b4a5968778695a4b3c2d1e0f00112233445566778899"+
			"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", 16)
	if !ok {
		t.Fatal("could not parse the test modulus")
	}
	return n
}

// THE REGRESSION TEST. A restricted modulus must be refused on a disallowed
// chain even when presented under a completely different group name.
func TestModulusChainPolicyIsKeyedOnValueNotName(t *testing.T) {
	n := testModulus(t)
	withTestNetworkPin(t, "restricted-test-pin", n, 8000800)
	withChainIDFor(t, 7000700, true) // a DIFFERENT chain than the pin allows

	// Sanity: keyed on the name, this is refused. (Pre-existing behaviour.)
	if err := enforceNetworkPinChainPolicy("restricted-test-pin"); err == nil {
		t.Fatal("the name-keyed guard must refuse a restricted pin on a disallowed chain")
	}

	// THE BYPASS: same modulus, foreign name. Must still be refused.
	err := enforceModulusChainPolicy(n)
	if err == nil {
		t.Fatal("D-35 REGRESSION: a restricted modulus was permitted on a disallowed chain. " +
			"The guard is keyed on the group name again, so renaming the modulus evades it — " +
			"which is how the trapdoored devnet modulus reaches mainnet under the name " +
			"rsa-2048-frc.")
	}
	if !errors.Is(err, ErrNetworkPinChainNotAllowed) {
		t.Fatalf("wrong error type %T (%v), want ErrNetworkPinChainNotAllowed", err, err)
	}
	// The message must say WHY, or an operator cannot act on it.
	if !strings.Contains(err.Error(), "MODULUS DIGEST") {
		t.Errorf("the refusal should state that the match was by digest, not name; got: %v", err)
	}
}

// The same modulus on its ALLOWED chain must install — the guard must restrict,
// not simply refuse everything.
func TestModulusChainPolicyPermitsItsOwnChain(t *testing.T) {
	n := testModulus(t)
	withTestNetworkPin(t, "restricted-test-pin", n, 8000800)
	withChainIDFor(t, 8000800, true)

	if err := enforceModulusChainPolicy(n); err != nil {
		t.Fatalf("a restricted modulus must be permitted on its own allowed chain: %v", err)
	}
}

// A modulus no pin claims is not this guard's business.
func TestModulusChainPolicyIgnoresUnpinnedModuli(t *testing.T) {
	withChainIDFor(t, 7000700, true)
	if err := enforceModulusChainPolicy(testModulus(t)); err != nil {
		t.Fatalf("an unrestricted modulus must pass the chain guard untouched: %v", err)
	}
}

// An unknown chain id must fail closed for a restricted modulus — the compiled
// default chain id is the devnet's, so assuming it would permit the modulus
// this guard exists to stop.
func TestModulusChainPolicyFailsClosedOnUnknownChain(t *testing.T) {
	n := testModulus(t)
	withTestNetworkPin(t, "restricted-test-pin", n, 8000800)
	withChainIDFor(t, 0, false)

	if err := enforceModulusChainPolicy(n); err == nil {
		t.Fatal("an unknown chain id must fail CLOSED for a restricted modulus, not assume a default")
	}
}

// buildVDFGroup must refuse end-to-end, with the override set — the exact
// configuration that shipped the bypass. This is the test that would have
// caught D-35.
func TestBuildVDFGroupRefusesRestrictedModulusUnderForeignNameWithOverride(t *testing.T) {
	n := testModulus(t)
	withTestNetworkPin(t, "restricted-test-pin", n, 8000800)
	withChainIDFor(t, 7000700, true)
	t.Setenv(allowUnpinnedModulusEnv, "1") // the override that used to wave it through

	_, err := buildVDFGroup(n, "some-unrelated-name")
	if err == nil {
		t.Fatal("D-35 REGRESSION: buildVDFGroup installed a chain-restricted modulus under a " +
			"foreign name because JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS was set. The override may " +
			"waive the LIBRARY digest requirement; it must never waive chain policy.")
	}
	if !errors.Is(err, ErrNetworkPinChainNotAllowed) {
		t.Fatalf("wrong error %T (%v), want ErrNetworkPinChainNotAllowed", err, err)
	}
}
