package syncmonitor

import "testing"

// A forced gap signal must reconcile even when the head gap is within the
// propagation tolerance (delta=1), and must not depend on the consecutive
// out-of-sync threshold. Guards the 2026-10-13 one-block-gap deadlock.
func TestGapSignalBypassesPropagationTolerance(t *testing.T) {
	if defaultPropagationToleranceBlocks < 1 {
		t.Skip("tolerance disabled; nothing to bypass")
	}
	m := &Monitor{propagationToleranceBlocks: defaultPropagationToleranceBlocks, outOfSyncThreshold: 2}
	// Exercise the decision logic in isolation: with forced=true a delta of 1 is a
	// reconcile, with forced=false it is excused.
	head, seq := uint64(1761), uint64(1762)
	if excused := !forcedReconcile(m, head, seq, false); !excused {
		t.Fatalf("unforced delta=1 should be excused as propagation lag")
	}
	if !forcedReconcile(m, head, seq, true) {
		t.Fatalf("forced delta=1 must reconcile")
	}
}
