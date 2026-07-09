package DB_OPs

import "testing"

// TestNextLatestBlock pins the monotonic tip-marker rule: the marker
// never regresses, whatever order blocks are stored in (PoTS WAL dumps,
// replays, stale catchup batches committing after newer live blocks).
func TestNextLatestBlock(t *testing.T) {
	cases := []struct {
		name               string
		current, candidate uint64
		want               uint64
		advance            bool
	}{
		{"advance forward", 100, 101, 101, true},
		{"replayed old block no-op", 100, 50, 100, false},
		{"same value no-op", 100, 100, 100, false},
		{"large jump allowed (catchup phase 8)", 100, 5000, 5000, true},
		{"genesis on fresh node no-op at 0", 0, 0, 0, false},
		{"first real block from genesis", 0, 1, 1, true},
	}
	for _, c := range cases {
		got, adv := nextLatestBlock(c.current, c.candidate)
		if got != c.want || adv != c.advance {
			t.Errorf("%s: nextLatestBlock(%d, %d) = (%d, %v), want (%d, %v)",
				c.name, c.current, c.candidate, got, adv, c.want, c.advance)
		}
	}
}
