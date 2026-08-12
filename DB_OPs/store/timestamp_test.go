package store

import (
	"testing"
	"time"
)

// TestNormalizeUpdatedAtNanos_Tiers pins the mixed-unit detection. Must match
// DB_OPs.normalizeUpdatedAtNanos (merge_account.go) behaviourally.
func TestNormalizeUpdatedAtNanos_Tiers(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, 0},
		{"negative", -5, -5},
		{"seconds", 1_700_000_000, 1_700_000_000 * int64(time.Second)},
		{"millis", 1_700_000_000_000, 1_700_000_000_000 * int64(time.Millisecond)},
		{"micros", 1_700_000_000_000_000, 1_700_000_000_000_000 * int64(time.Microsecond)},
		{"nanos", 1_700_000_000_000_000_000, 1_700_000_000_000_000_000},
	}
	for _, c := range cases {
		if got := NormalizeUpdatedAtNanos(c.in); got != c.want {
			t.Errorf("%s: NormalizeUpdatedAtNanos(%d) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

// TestNormalizedUnixTime_LWWOrdering is the §3a regression: a nanos-stamped
// sync write and a seconds-stamped live write for the SAME wall-clock instant
// must compare equal-ish (same second), NOT 9 orders of magnitude apart. The
// pre-fix code (time.Unix(0, seconds)) put the live write in 1970.
func TestNormalizedUnixTime_LWWOrdering(t *testing.T) {
	instant := int64(1_700_000_050)                      // a real second
	liveSeconds := instant                               // live executor stamp
	syncNanos := instant*int64(time.Second) + 123        // sync stamp, same instant +123ns

	live := NormalizedUnixTime(liveSeconds)
	sync := NormalizedUnixTime(syncNanos)

	// Both must land in 2023, not 1970.
	if live.Year() < 2020 {
		t.Fatalf("live timestamp fell to %v — seconds interpreted as nanos (the §3a bug)", live)
	}
	// The sync write is newer (by 123ns) — must sort strictly after the live one.
	if !sync.After(live) {
		t.Fatalf("sync (%v) should be After live (%v) — LWW ordering broken", sync, live)
	}
	// They must be within one second, not decades apart.
	if sync.Sub(live) > time.Second {
		t.Fatalf("normalized timestamps %v and %v differ by more than 1s", sync, live)
	}
}

func TestNormalizedUnixTime_ZeroIsZeroTime(t *testing.T) {
	if !NormalizedUnixTime(0).IsZero() {
		t.Error("NormalizedUnixTime(0) must be the zero time")
	}
}
