package config

import "testing"

// The period cap is MaxPeriod at every height, including heights whose
// blocks were produced under the earlier cap of 1024.
func TestMaxPeriodAt(t *testing.T) {
	for _, h := range []uint64{0, 1, 500, 1 << 40} {
		if got := MaxPeriodAt(h); got != MaxPeriod {
			t.Fatalf("MaxPeriodAt(%d) = %d, want %d", h, got, MaxPeriod)
		}
	}
}
