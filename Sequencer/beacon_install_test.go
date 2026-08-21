package Sequencer

import (
	"crypto/rand"
	"math/big"
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
