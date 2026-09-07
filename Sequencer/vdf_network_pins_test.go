package Sequencer

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// withNetworkPin registers a temporary jmdn network pin for the test's lifetime.
func withNetworkPin(t *testing.T, rec vdf.ProvenanceRecord) {
	t.Helper()
	if _, exists := networkVDFPins[rec.Name]; exists {
		t.Fatalf("test pin name %q collides with a shipped pin", rec.Name)
	}
	networkVDFPins[rec.Name] = rec
	t.Cleanup(func() { delete(networkVDFPins, rec.Name) })

	// Every pin needs a policy, or the D-29 chain guard refuses it — which is
	// the point of that guard, and these tests are about digest/shape handling
	// rather than chain policy. A test fixture modulus has no trapdoor holder,
	// so an unrestricted policy is the honest declaration for it.
	networkPinPolicies[rec.Name] = networkPinPolicy{TrapdoorKnown: false}
	t.Cleanup(func() { delete(networkPinPolicies, rec.Name) })
}

// A modulus pinned by jmdn (not by the avc library) installs with NO override:
// the network pin is a first-class pin, not an escape hatch.
func TestInstallAVCBeaconFromEnv_NetworkPin_MatchingDigest_InstallsWithoutOverride(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	digest, err := vdf.ModulusDigest(n)
	if err != nil {
		t.Fatal(err)
	}
	withNetworkPin(t, vdf.ProvenanceRecord{
		Name: "test-network-pin", Bits: n.BitLen(), Digits: len(n.String()),
		Digest: digest, Source: "test fixture", Note: "test only",
	})
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-network-pin")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "") // no override — the pin must suffice

	installed, err := InstallAVCBeaconFromEnv()
	if err != nil {
		t.Fatalf("network-pinned modulus should install without the override: %v", err)
	}
	if !installed || activeVDFPipeline() == nil {
		t.Fatal("expected the beacon pipeline to be installed")
	}
}

// A name jmdn pins, supplied with a DIFFERENT modulus, is refused — and the
// unpinned override does not rescue it. That is the wrong-but-plausible case
// pinning exists to catch; waiving it under a known name would defeat the pin.
func TestInstallAVCBeaconFromEnv_NetworkPin_WrongModulus_RefusedEvenWithOverride(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	withNetworkPin(t, vdf.ProvenanceRecord{
		Name: "test-network-pin", Bits: n.BitLen(), Digits: len(n.String()),
		Digest: strings.Repeat("00", 32), // deliberately not this modulus
		Source: "test fixture", Note: "test only",
	})
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-network-pin")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1") // must NOT bypass a network pin

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil || installed {
		t.Fatalf("expected refusal for a modulus that does not match its network pin; got installed=%v err=%v", installed, err)
	}
	if !errors.Is(err, ErrNetworkPinMismatch) {
		t.Fatalf("expected ErrNetworkPinMismatch, got: %v", err)
	}
	if activeVDFPipeline() != nil {
		t.Fatal("pipeline must not be installed after a refused modulus")
	}
}

// Names unknown to BOTH registries keep the pre-existing behaviour: refused by
// default, allowed only via the explicit override (covered by the tests in
// beacon_install_test.go). This guards that adding network pins did not widen it.
func TestInstallAVCBeaconFromEnv_UnknownEverywhere_StillRefusedByDefault(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "no-such-group-anywhere")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil || installed {
		t.Fatalf("expected refusal for an unknown, unpinned name; got installed=%v err=%v", installed, err)
	}
}

// The shipped devnet/testnet pin is well-formed: RSA-2048 shape, a 64-hex
// sha256 digest, and an honest Note. It must never be mistaken for a sourced
// modulus, so the Note has to say so.
func TestShippedNetworkPins_WellFormed(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for name, rec := range networkVDFPins {
		if rec.Name != name {
			t.Errorf("%q: record Name %q does not match its map key", name, rec.Name)
		}
		if rec.Bits != vdf.RSA2048Bits || rec.Digits != vdf.RSA2048Digits {
			t.Errorf("%q: expected RSA-2048 shape (%d bits / %d digits), got %d / %d",
				name, vdf.RSA2048Bits, vdf.RSA2048Digits, rec.Bits, rec.Digits)
		}
		if !hex64.MatchString(rec.Digest) {
			t.Errorf("%q: digest %q is not 64 lowercase hex chars", name, rec.Digest)
		}
		if rec.Source == "" || rec.Note == "" {
			t.Errorf("%q: Source and Note are mandatory for a network pin", name)
		}
		if _, inLibrary := vdf.LookupProvenance(name); inLibrary {
			t.Errorf("%q: also present in the avc library registry — pin it in exactly one place", name)
		}
	}
	if _, ok := networkVDFPins["rsa-2048-testnet-ephemeral"]; !ok {
		t.Fatal("the devnet/testnet pin rsa-2048-testnet-ephemeral is missing")
	}
}
