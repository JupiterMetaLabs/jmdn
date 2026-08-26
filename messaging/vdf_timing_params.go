package messaging

// VDF timing self-consistency check — Architecture §10 decision 12b, found
// 2026-08-20: S was published as the rounded 6.67, but the adopted
// T_vdf=1200s only sits on the bias-resistance floor at the EXACT fraction
// 20/3 (S=6.67 misses it by 0.6s). avc/vdf.CheckBiasResistance and
// CheckLiveness were built to catch exactly this class of bug but had zero
// production callers anywhere in either repo — this file is that caller,
// same shape as ValidateFallbackWindowParams below/alongside it.
//
// # Why this lives here, not inside avc/beacon.Pipeline.Accept/New
//
// CheckBiasResistance/CheckLiveness take a wall-clock time.Duration, but
// Pipeline.difficulty (and proof.T) is a squaring COUNT, not a duration —
// there is no wall-clock value on Pipeline to check per-call, and deriving
// one at runtime would mean running vdf.Calibrate-style timing measurement
// at startup, which beacon_install.go's own header already rules out. This
// is a check that the ADOPTED PROTOCOL CONSTANTS are mutually consistent,
// not a per-proof runtime verification — so it belongs with the other
// epoch-timing constants (N, RevealCutoffK) and their own self-consistency
// checker (ValidateFallbackWindowParams), not inside the VDF accept path.
import (
	"fmt"
	"time"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// SlotFloor is s_min — the assumed minimum wall-clock slot duration every
// other epoch-timing constant in this package (FallbackFoldBufferB,
// FallbackFoldMaxSlotOffset, and ValidateVDFTimingParams below) is derived
// against. NOT enforced anywhere yet — Architecture §10 decision 3b is
// still open: slots advance on block commit (SlotStore.AdvanceOnCommit),
// there is no wall-clock floor in the consensus round timer. This constant
// makes that already-relied-upon assumption explicit and checkable instead
// of leaving it as unlinked doc-comment arithmetic repeated in three files.
const SlotFloor = 60 * time.Second

// TargetVDFDelay is T_vdf, the design-target wall-clock VDF evaluation time
// (VDF-Implementation-Handoff.md §0, target band 1200-1410s; this is the
// floor of that band, where the adopted S=20/3 sits at exact equality).
//
// This is NOT derived from JMDN_AVC_VDF_DIFFICULTY_T. That env var
// (jmdn/Sequencer/beacon_install.go) is a squaring COUNT, calibrated
// offline via vdf.Calibrate on the slowest fleet hardware to approximate
// this duration — TargetVDFDelay is the policy input that calibration is
// aimed at, checked here for self-consistency with
// RevealCutoffK/N/SlotFloor, not a live measurement of what the installed
// difficulty actually produces on any given node.
const TargetVDFDelay = 1200 * time.Second

// ValidateVDFTimingParams checks that the adopted epoch-timing parameters
// (N, RevealCutoffK, SlotFloor, TargetVDFDelay) satisfy avc/vdf's
// bias-resistance and liveness bounds. Same shape and same reason as
// ValidateFallbackWindowParams: catches a parameter-drift bug like §10
// decision 12b's rounded 6.67 before it ships silently, by using
// vdf.AdoptedSpeedup() (the exact 20/3 fraction) rather than a decimal
// literal. Call once at startup, alongside ValidateFallbackWindowParams.
func ValidateVDFTimingParams() error {
	revealWindow := time.Duration(RevealCutoffK) * SlotFloor
	epochRunway := time.Duration(N-RevealCutoffK) * SlotFloor
	if err := vdf.CheckBiasResistance(TargetVDFDelay, revealWindow, vdf.AdoptedSpeedup()); err != nil {
		return fmt.Errorf("messaging: adopted VDF timing parameters (T_vdf=%s, K=%d, s_min=%s, S=20/3) fail bias-resistance: %w",
			TargetVDFDelay, RevealCutoffK, SlotFloor, err)
	}
	if err := vdf.CheckLiveness(TargetVDFDelay, epochRunway); err != nil {
		return fmt.Errorf("messaging: adopted VDF timing parameters (T_vdf=%s, N=%d, K=%d, s_min=%s) fail liveness: %w",
			TargetVDFDelay, N, RevealCutoffK, SlotFloor, err)
	}
	return nil
}
