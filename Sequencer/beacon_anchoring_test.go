package Sequencer

import (
	"errors"
	"testing"
)

// Reveals are BLS signatures under snapshot-bound keys (A-2), and only a
// chain-anchored pool gives every node the same keys, so the Stage-2 beacon
// refuses to install without committee anchoring.
func TestCheckRevealAnchoring(t *testing.T) {
	if err := checkRevealAnchoring(true); err != nil {
		t.Fatalf("anchoring on must pass: %v", err)
	}
	if err := checkRevealAnchoring(false); !errors.Is(err, ErrBeaconNeedsAnchoring) {
		t.Fatalf("no anchoring must be refused, got %v", err)
	}
}

// withBeaconPrereqs makes the anchoring and agg_cert checks pass for a test
// that installs a beacon (these tests load no settings), restoring the real
// checks after.
func withBeaconPrereqs(t *testing.T) {
	t.Helper()
	prevA, prevC := beaconAnchoringEnabled, beaconAggCertEnabled
	beaconAnchoringEnabled = func() bool { return true }
	beaconAggCertEnabled = func() bool { return true }
	t.Cleanup(func() { beaconAnchoringEnabled, beaconAggCertEnabled = prevA, prevC })
}

// End to end: a fully valid beacon configuration is still refused when
// anchoring is off, and nothing is installed.
func TestInstallAVCBeaconFromEnv_RefusedWithoutAnchoring(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")
	withBeaconPrereqs(t)
	beaconAnchoringEnabled = func() bool { return false }

	installed, err := InstallAVCBeaconFromEnv()
	if !errors.Is(err, ErrBeaconNeedsAnchoring) || installed {
		t.Fatalf("installed=%v err=%v, want refused with ErrBeaconNeedsAnchoring", installed, err)
	}
	if activeVDFPipeline() != nil {
		t.Fatal("a refused install must not leave a pipeline behind")
	}
}

func TestCheckBeaconAggCert(t *testing.T) {
	if err := checkBeaconAggCert(true); err != nil {
		t.Fatalf("agg_cert on must pass: %v", err)
	}
	if err := checkBeaconAggCert(false); !errors.Is(err, ErrBeaconNeedsAggCert) {
		t.Fatalf("agg_cert off must be refused, got %v", err)
	}
}

// End to end: a valid beacon configuration with agg_cert off is refused, so
// no epoch can end up with no fallback seed.
func TestInstallAVCBeaconFromEnv_RefusedWithoutAggCert(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")
	withBeaconPrereqs(t)
	beaconAggCertEnabled = func() bool { return false }

	installed, err := InstallAVCBeaconFromEnv()
	if !errors.Is(err, ErrBeaconNeedsAggCert) || installed {
		t.Fatalf("installed=%v err=%v, want refused with ErrBeaconNeedsAggCert", installed, err)
	}
	if activeVDFPipeline() != nil {
		t.Fatal("a refused install must not leave a pipeline behind")
	}
}

// The installed pipeline seals and verifies under the chain's cut-over: the
// legacy VDF functions for entropy before VDFCutoverEpoch (so history
// verifies), the fixed ones from it.
func TestInstallAVCBeaconFromEnv_PipelineFollowsTheCutover(t *testing.T) {
	resetVDFWiringState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")
	withBeaconPrereqs(t)
	prev := vdfCutoverEpoch
	vdfCutoverEpoch = func() uint64 { return 53 }
	t.Cleanup(func() { vdfCutoverEpoch = prev })

	if installed, err := InstallAVCBeaconFromEnv(); err != nil || !installed {
		t.Fatalf("install: installed=%v err=%v", installed, err)
	}
	p := activeVDFPipeline()
	if !p.Legacy(52) || p.Legacy(53) || p.Legacy(1000) {
		t.Fatal("pipeline must use the legacy VDF functions only before the cut-over")
	}
}
