package reputation

// A4-COMPLETION-LLD.md §6 tests for Decision A4-1's remap.

import (
	"testing"
	"time"
)

// The worked examples from selection_weight.go's own doc comment — the
// contract this whole remap exists to satisfy.
func TestSelectionWeight_WorkedExamples(t *testing.T) {
	cases := []struct {
		name     string
		repScore float64
		want     float64
	}{
		{"never observed (Start)", Start, 0.70},
		{"ceiling (Cap, perfect history)", Cap, 0.94},
		{"floor (Floor, worst reachable)", Floor, faultedFloor},
		{"1 Absent", Start + Delta(Absent), 0.60},
		{"1 BadSignature", Start + Delta(BadSignature), 0.40},
		{"1 Equivocation (bottoms at Floor)", clamp(Start + Delta(Equivocation)), faultedFloor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SelectionWeight(c.repScore)
			if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("SelectionWeight(%v) = %v, want %v", c.repScore, got, c.want)
			}
		})
	}
}

// The two segments must meet exactly at Start with no discontinuity — a
// caller relying on "close reputation scores produce close selection
// weights" must never see a jump right at the neutral point.
func TestSelectionWeight_ContinuousAtStart(t *testing.T) {
	const epsilon = 1e-6
	below := SelectionWeight(Start - epsilon)
	at := SelectionWeight(Start)
	above := SelectionWeight(Start + epsilon)

	if at != healthyFloor {
		t.Fatalf("SelectionWeight(Start) = %v, want exactly healthyFloor (%v)", at, healthyFloor)
	}
	if d := at - below; d < 0 || d > 1e-3 {
		t.Errorf("discontinuity approaching Start from below: at=%v below=%v diff=%v", at, below, d)
	}
	if d := above - at; d < 0 || d > 1e-3 {
		t.Errorf("discontinuity approaching Start from above: at=%v above=%v diff=%v", at, above, d)
	}
}

// Monotonic across the full domain — a higher reputation score must never
// map to a lower (or equal-but-out-of-order) selection weight. Guards
// against a future constant change accidentally breaking the ranking.
func TestSelectionWeight_Monotonic(t *testing.T) {
	prev := SelectionWeight(Floor)
	const steps = 200
	for i := 1; i <= steps; i++ {
		repScore := Floor + (Cap-Floor)*float64(i)/float64(steps)
		got := SelectionWeight(repScore)
		if got < prev {
			t.Fatalf("SelectionWeight regressed at repScore=%v: got %v, previous step was %v", repScore, got, prev)
		}
		prev = got
	}
}

// Input outside [Floor, Cap] (a caller bug — Store.Score/Snapshot should
// never produce this) must still return a value inside the function's own
// valid output range, never something further out of bounds.
func TestSelectionWeight_ClampsOutOfRangeInput(t *testing.T) {
	if got := SelectionWeight(-1.0); got != faultedFloor {
		t.Errorf("SelectionWeight(-1.0) = %v, want clamped to faultedFloor (%v)", got, faultedFloor)
	}
	if got := SelectionWeight(5.0); got != healthyCeil {
		t.Errorf("SelectionWeight(5.0) = %v, want clamped to healthyCeil (%v)", got, healthyCeil)
	}
}

// Fixed clock throughout: Score/Snapshot decay by elapsed wall-clock time,
// so comparing two separately-timed live reads of the same store is
// inherently flaky by a few ULPs even when nothing changed in between.
// NewStoreWithClock exists precisely so a test can hold time still instead.
func TestSnapshotSelectionWeights_RemapsEveryObservedPeer(t *testing.T) {
	orig := Default
	now := time.Now()
	Default = NewStoreWithClock(func() time.Time { return now })
	defer func() { Default = orig }()

	Default.Observe("peerA", AgreeFinalized) // Start + 0.02
	Default.Observe("peerB", Equivocation)   // Start -> Floor

	raw := Default.Snapshot()
	got := SnapshotSelectionWeights()
	if len(got) != 2 {
		t.Fatalf("got %d peers, want 2: %v", len(got), got)
	}
	wantA := SelectionWeight(raw["peerA"])
	wantB := SelectionWeight(raw["peerB"])
	if got["peerA"] != wantA {
		t.Errorf("peerA: got %v, want %v (raw remapped)", got["peerA"], wantA)
	}
	if got["peerB"] != wantB {
		t.Errorf("peerB: got %v, want %v (raw remapped)", got["peerB"], wantB)
	}
	if got["peerB"] >= 0.5 {
		t.Errorf("an equivocating peer must land below the 0.5 eligibility floor, got %v", got["peerB"])
	}
}
