package reputation

import "testing"

func TestRoundHealth_ColdStartAssumesHealthy(t *testing.T) {
	h := NewRoundHealth(20)
	// Fewer than minSamples observations → assume healthy (do not suspend).
	h.Record(false)
	h.Record(false)
	if h.ShouldSuspendPush(0.5, 5) {
		t.Fatal("cold start (n<minSamples) must NOT suspend the push")
	}
}

func TestRoundHealth_SuspendsWhenFailing(t *testing.T) {
	h := NewRoundHealth(10)
	for i := 0; i < 10; i++ {
		h.Record(false) // sequencer's own rounds all failing
	}
	if got := h.SuccessRate(5); got != 0 {
		t.Fatalf("success rate = %v, want 0", got)
	}
	if !h.ShouldSuspendPush(0.5, 5) {
		t.Fatal("with a 0%% success rate the push MUST be suspended (D-61)")
	}
}

func TestRoundHealth_ResumesWhenHealthy(t *testing.T) {
	h := NewRoundHealth(10)
	for i := 0; i < 10; i++ {
		h.Record(true)
	}
	if h.ShouldSuspendPush(0.5, 5) {
		t.Fatal("healthy sequencer must push")
	}
}

func TestRoundHealth_WindowRollover(t *testing.T) {
	h := NewRoundHealth(4)
	// 4 fails then 4 successes: window holds only the last 4 → all success.
	for i := 0; i < 4; i++ {
		h.Record(false)
	}
	for i := 0; i < 4; i++ {
		h.Record(true)
	}
	if got := h.SuccessRate(1); got != 1.0 {
		t.Fatalf("after rollover success rate = %v, want 1.0", got)
	}
}
