package reputation

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	s := NewStoreWithClock(func() time.Time { return fixed })
	// A faulted peer and a rewarded peer.
	s.Observe("peerBad", BadSignature)    // 0.50 - 0.30 = 0.20
	s.Observe("peerGood", AgreeFinalized) // 0.50 + 0.02 = 0.52

	path := filepath.Join(t.TempDir(), "rep.json")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2 := NewStoreWithClock(func() time.Time { return fixed })
	if err := s2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, id := range []string{"peerBad", "peerGood"} {
		if a, b := s.Score(id), s2.Score(id); math.Abs(a-b) > 1e-9 {
			t.Fatalf("score for %s not preserved: saved %.6f loaded %.6f", id, a, b)
		}
	}
	// Specifically: the faulted peer must NOT come back at Start (the restart bug).
	if s2.Score("peerBad") >= Start {
		t.Fatalf("D-62: faulted peer reset to/above Start on load (%.3f) — restart bug not fixed", s2.Score("peerBad"))
	}
}

func TestStore_LoadMissingFileIsEmptyNotError(t *testing.T) {
	s := NewStore()
	if err := s.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("missing file must load as empty, got error: %v", err)
	}
	if got := s.Score("anyone"); got != Start {
		t.Fatalf("unknown peer after empty load should be Start, got %.3f", got)
	}
}
