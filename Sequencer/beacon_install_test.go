package Sequencer

import (
	"crypto/rand"
	"math/big"
	"strings"
	"testing"
)

// testFixtureModulus generates a throwaway >=2048-bit RSA-shaped modulus for
// test purposes ONLY. This is explicitly NOT the production case
// beacon_install.go's header warns about: nobody is relying on this value's
// factorisation being unknown, it never leaves this test process, and it
// exists only to exercise ValidateModulus/NewRSAGroup's mechanical checks
// end-to-end. Generating a real network modulus this way would be exactly
// the "NOT ACCEPTABLE" path avc/vdf.go's package doc describes.
func testFixtureModulus(t *testing.T) *big.Int {
	t.Helper()
	p, err := rand.Prime(rand.Reader, 1030)
	if err != nil {
		t.Fatalf("generating test fixture prime p: %v", err)
	}
	q, err := rand.Prime(rand.Reader, 1030)
	if err != nil {
		t.Fatalf("generating test fixture prime q: %v", err)
	}
	return new(big.Int).Mul(p, q)
}

func TestInstallAVCBeaconFromEnv_NotConfigured_NoErrorNotInstalled(t *testing.T) {
	resetVDFWiringState(t)
	// Deliberately leave all three required env vars unset.
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", "")
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "")

	installed, err := InstallAVCBeaconFromEnv()
	if err != nil {
		t.Fatalf("unset config must never be an error, got: %v", err)
	}
	if installed {
		t.Fatal("installed=true with no configuration present")
	}
	if activeVDFPipeline() != nil {
		t.Fatal("no pipeline should have been installed")
	}
}

func TestInstallAVCBeaconFromEnv_InvalidHex_ReturnsError(t *testing.T) {
	resetVDFWiringState(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", "not-hex-at-all")
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "1000")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("expected an error for invalid hex, got nil")
	}
	if installed {
		t.Fatal("installed must be false on error")
	}
}

func TestInstallAVCBeaconFromEnv_ModulusTooSmall_ReturnsError(t *testing.T) {
	resetVDFWiringState(t)
	// 15 (0xF) is nowhere near vdf.MinModulusBits (2048) — must be rejected
	// by ValidateModulus, proving that check is actually wired in, not
	// bypassed.
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", "f")
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "1000")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("expected an error for an undersized modulus, got nil")
	}
	if installed {
		t.Fatal("installed must be false on error")
	}
}

func TestInstallAVCBeaconFromEnv_ZeroDifficulty_ReturnsError(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "0")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("expected an error for zero difficulty, got nil")
	}
	if installed {
		t.Fatal("installed must be false on error")
	}
}

func TestInstallAVCBeaconFromEnv_AllValid_InstallsPipelineAndHook(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2") // tiny — this is a wiring test, not a delay test
	// A generated fixture modulus is by definition unpinned, so this wiring
	// test must take the documented escape hatch. That is the legitimate
	// local/test case the override exists for — see
	// TestInstallAVCBeaconFromEnv_UnpinnedModulus_RefusedByDefault for the
	// production behaviour it is opting out of.
	t.Setenv(allowUnpinnedModulusEnv, "1")

	installed, err := InstallAVCBeaconFromEnv()
	if err != nil {
		t.Fatalf("unexpected error with a valid fixture: %v", err)
	}
	if !installed {
		t.Fatal("expected installed=true with a fully valid configuration")
	}
	if activeVDFPipeline() == nil {
		t.Fatal("SetVDFPipeline was not called — activeVDFPipeline() is nil after a successful install")
	}
}

// The security property this file's pinning exists for: a modulus that
// passes every mechanical check is STILL refused unless somebody verified it
// against a primary source and pinned its digest. Without this, a
// self-generated modulus — whose factorisation the generator holds, letting
// them evaluate the VDF instantly and steer committee selection — installs
// silently and looks completely healthy.
func TestInstallAVCBeaconFromEnv_UnpinnedModulus_RefusedByDefault(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "") // the default: no override

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("an unpinned modulus was installed with no override set")
	}
	if installed {
		t.Fatal("installed must be false when the modulus is refused")
	}
	if activeVDFPipeline() != nil {
		t.Fatal("a pipeline was installed despite the refusal")
	}
	// The error is the operator's only guidance at this point; it must name
	// the override rather than leaving them to grep for it.
	if !strings.Contains(err.Error(), allowUnpinnedModulusEnv) {
		t.Errorf("the refusal should name the override env var, got: %v", err)
	}
}

// A group name that IS in the registry but is not yet pinned must also be
// refused — otherwise "rsa-2048-frc" would be accepted before anyone had
// verified the number behind it, which is the failure mode the whole
// registry exists to prevent.
func TestInstallAVCBeaconFromEnv_KnownButUnpinnedName_StillRefused(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "rsa-2048-frc")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("a registry name with no pinned digest was accepted")
	}
	if installed {
		t.Fatal("installed must be false when the modulus is refused")
	}
}

// The override waives the pinned digest, not the whole provenance check.
// Closing a gap found in review: NewRSAGroup runs ValidateModulus only, so
// without an explicit shape check here, a modulus of visibly the wrong size
// would install under a registry name that documents exactly what size it
// should be.
func TestInstallAVCBeaconFromEnv_UnpinnedOverride_StillEnforcesKnownShape(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t) // ~2060 bits, not RSA-2048's 2048
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "rsa-2048-frc")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")

	installed, err := InstallAVCBeaconFromEnv()
	if err == nil {
		t.Fatal("a wrong-sized modulus installed under a known registry name")
	}
	if installed {
		t.Fatal("installed must be false when the shape check fails")
	}
}

// The same override on a name the registry does NOT know must still work —
// that is the local/testnet case it exists for, where no published shape is
// on file to check against.
func TestInstallAVCBeaconFromEnv_UnpinnedOverride_AllowsUnknownName(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "local-testnet-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")

	installed, err := InstallAVCBeaconFromEnv()
	if err != nil {
		t.Fatalf("the local/testnet override path must still work: %v", err)
	}
	if !installed {
		t.Fatal("expected installed=true on the override path with an unknown name")
	}
}
