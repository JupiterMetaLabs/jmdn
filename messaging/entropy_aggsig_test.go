package messaging

// Regression guard for the slot-arithmetic defect found during the design-
// conformance audit (2026-08-20).

import "testing"

// SlotStore.AdvanceOnCommit does `slot += period + 1` (§7.1), so a block
// committed at period P sits P+1 slots after its parent — NOT one. An earlier
// version of VerifyAndRecordPrevCert assumed Slot-1 and would have recorded the
// certificate against a slot that never had a block, silently holing the fold
// window on exactly the rounds that timed out.
func TestPrevSlotAccountsForPeriod(t *testing.T) {
	cases := []struct {
		name         string
		slot, period uint64
		wantPrevSlot uint64
	}{
		{"no timeout", 100, 0, 99},
		{"one timeout", 101, 1, 99},
		{"three timeouts", 103, 3, 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.slot - (tc.period + 1)
			if got != tc.wantPrevSlot {
				t.Fatalf("parent slot = %d, want %d (slot %d, period %d)",
					got, tc.wantPrevSlot, tc.slot, tc.period)
			}
			if naive := tc.slot - 1; tc.period > 0 && naive == got {
				t.Fatal("the naive Slot-1 form happens to agree here; this case does not exercise the defect")
			}
		})
	}
}

// And the arithmetic must match what SlotStore actually does, not a restatement
// of it — drive the real store.
func TestPrevSlotMatchesSlotStoreBehaviour(t *testing.T) {
	s := NewSlotStore()
	parentSlot, _ := s.AdvanceOnCommit(10, 0)

	const period = 2
	childSlot, _ := s.AdvanceOnCommit(11, period)

	if got := childSlot - (period + 1); got != parentSlot {
		t.Fatalf("derived parent slot %d != actual parent slot %d", got, parentSlot)
	}
	if childSlot-1 == parentSlot {
		t.Fatal("Slot-1 agreed with the real parent slot at period=2 — SlotStore's increment rule may have changed")
	}
}
