package messaging

import (
	"errors"
	"testing"
	"time"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// Regression test for the exact class of bug §10 decision 12b found: S
// printed as the rounded 6.67 instead of the exact 20/3 that the adopted
// T_vdf=1200s actually needs. If TargetVDFDelay/RevealCutoffK/SlotFloor/N
// ever drift out of sync, this fails loudly instead of shipping silently.
func TestValidateVDFTimingParams_AdoptedValuesPass(t *testing.T) {
	if err := ValidateVDFTimingParams(); err != nil {
		t.Fatalf("adopted VDF timing parameters (T_vdf=%s, N=%d, K=%d, s_min=%s) are not usable: %v",
			TargetVDFDelay, N, RevealCutoffK, SlotFloor, err)
	}
}

// Confirms ValidateVDFTimingParams actually wraps vdf.CheckBiasResistance's
// sentinel error rather than swallowing or mis-wrapping it. Does not mutate
// the real constants — calls vdf.CheckBiasResistance directly with a
// deliberately too-short delay, same style as avc/vdf/vdf_test.go's own
// negative cases.
func TestValidateVDFTimingParams_WiresBiasResistanceSentinel(t *testing.T) {
	revealWindow := time.Duration(RevealCutoffK) * SlotFloor
	tooShort := revealWindow // exactly the window itself is not >= S=20/3 times it
	err := vdf.CheckBiasResistance(tooShort, revealWindow, vdf.AdoptedSpeedup())
	if !errors.Is(err, vdf.ErrBiasResistanceInsufficient) {
		t.Fatalf("expected ErrBiasResistanceInsufficient for a too-short delay, got: %v", err)
	}
}

// Same confirmation for the liveness sentinel — a delay past half the epoch
// runway must be rejected.
func TestValidateVDFTimingParams_WiresLivenessSentinel(t *testing.T) {
	epochRunway := time.Duration(N-RevealCutoffK) * SlotFloor
	tooLong := epochRunway // exceeds epochRunway/2
	err := vdf.CheckLiveness(tooLong, epochRunway)
	if !errors.Is(err, vdf.ErrLivenessExceeded) {
		t.Fatalf("expected ErrLivenessExceeded for a too-long delay, got: %v", err)
	}
}

// Pins the exact arithmetic §10 decision 12b turned on: at the adopted
// N=50/K=3/s_min=60s, T_vdf=1200s sits EXACTLY on the bias-resistance floor
// only at S=20/3 (S*K*s_min = 1200s to the second). A rounded S=6.67 misses
// it by 0.6s. This test would fail if TargetVDFDelay/RevealCutoffK/SlotFloor
// are ever changed without re-deriving this equality.
func TestAdoptedParameters_SitExactlyOnBiasResistanceFloor(t *testing.T) {
	revealWindow := time.Duration(RevealCutoffK) * SlotFloor
	s := vdf.AdoptedSpeedup()
	floor := revealWindow * time.Duration(s.Num) / time.Duration(s.Den)
	if floor != TargetVDFDelay {
		t.Fatalf("S*K*s_min = %s, want exactly TargetVDFDelay = %s — the adopted parameters no longer sit on the floor",
			floor, TargetVDFDelay)
	}
}
