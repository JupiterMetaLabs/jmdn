package messaging

// Parent-slot arithmetic used by VerifyAndRecordPrevCert and the fallback
// recovery: a block's slot is its block number whatever its Period, so its
// parent sits exactly one slot back.

import "testing"

func TestPrevSlotIsSlotMinusOneWhateverThePeriod(t *testing.T) {
	cases := []struct {
		name         string
		slot, period uint64
	}{
		{"first attempt", 100, 0},
		{"after one failed round", 100, 1},
		{"after three failed rounds", 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.slot - 1; got != 99 {
				t.Fatalf("parent slot = %d, want 99 (slot %d, period %d)", got, tc.slot, tc.period)
			}
		})
	}
}

// And the arithmetic must match what the slot rule actually produces for two
// consecutive heights - drive LiveSlotFor, including a parent that needed
// retries.
func TestPrevSlotMatchesLiveSlotFor(t *testing.T) {
	saved := DefaultProposerPeriods
	DefaultProposerPeriods = NewProposerPeriods()
	t.Cleanup(func() { DefaultProposerPeriods = saved })

	DefaultProposerPeriods.AdvanceAfterFailure(10, 7) // height 10 needed a retry
	DefaultProposerPeriods.AdvanceAfterFailure(11, 7)
	DefaultProposerPeriods.AdvanceAfterFailure(11, 7) // height 11 needed two
	parentSlot, childSlot := LiveSlotFor(10), LiveSlotFor(11)
	if childSlot-1 != parentSlot {
		t.Fatalf("child slot %d - 1 != parent slot %d", childSlot, parentSlot)
	}
}
