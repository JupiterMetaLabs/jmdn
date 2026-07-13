package DB_OPs

import (
	"testing"
	"time"
)

// TestNormalizeUpdatedAtNanos verifies unit-safe LWW timestamp comparison:
// live-executor stamps (block timestamp, Unix seconds) and sync-path stamps
// (UnixNano) must be comparable after normalization.
func TestNormalizeUpdatedAtNanos(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, 0},
		{"negative", -5, -5},
		{"seconds", base.Unix(), base.UnixNano()},
		{"millis", base.UnixMilli(), base.UnixNano()},
		{"micros", base.UnixMicro(), base.UnixNano()},
		{"nanos", base.UnixNano(), base.UnixNano()},
	}
	for _, c := range cases {
		if got := normalizeUpdatedAtNanos(c.in); got != c.want {
			t.Errorf("%s: normalizeUpdatedAtNanos(%d) = %d, want %d", c.name, c.in, got, c.want)
		}
	}

	// Ordering: a live write (seconds) one minute AFTER a sync write (nanos)
	// must win LWW after normalization. Raw comparison would invert this.
	syncWrite := base.UnixNano()
	liveWrite := base.Add(time.Minute).Unix()
	if !(normalizeUpdatedAtNanos(liveWrite) > normalizeUpdatedAtNanos(syncWrite)) {
		t.Errorf("later live write (seconds) must beat earlier sync write (nanos) after normalization")
	}
	if liveWrite > syncWrite {
		t.Errorf("sanity: raw comparison should be inverted (this is the bug normalization fixes)")
	}
}
