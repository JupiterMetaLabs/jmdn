package config

import "testing"

func TestMaxPeriodAt(t *testing.T) {
	t.Cleanup(func() { SetPeriodCapRaiseFrom(0) })
	SetPeriodCapRaiseFrom(0)
	if MaxPeriodAt(1<<40) != LegacyMaxPeriod {
		t.Fatal("raise off: cap must stay at the legacy value")
	}
	SetPeriodCapRaiseFrom(500)
	if MaxPeriodAt(499) != LegacyMaxPeriod || MaxPeriodAt(500) != MaxPeriod {
		t.Fatalf("activation: %d %d", MaxPeriodAt(499), MaxPeriodAt(500))
	}
}
