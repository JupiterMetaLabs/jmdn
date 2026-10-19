package messaging

import (
	"fmt"
	"testing"
)

func TestSeenMessages_Bounded(t *testing.T) {
	for i := 0; i < maxSeenMessages+500; i++ {
		markMessageSeen(fmt.Sprintf("id-%d", i))
	}
	if n := seenMessages.Len(); n > maxSeenMessages {
		t.Fatalf("seen cache holds %d, want <= %d", n, maxSeenMessages)
	}
	if !isMessageSeen(fmt.Sprintf("id-%d", maxSeenMessages+499)) {
		t.Fatal("most recent ID must still be remembered")
	}
}

func TestPeriodStore_PrunesOldHeights(t *testing.T) {
	s := NewPeriodStore()
	s.periods[5] = 1
	s.periods[3000] = 2
	s.pruneBelowLocked(3000)
	if _, ok := s.periods[5]; ok {
		t.Fatal("height far below the newest certified height must be pruned")
	}
	if s.periods[3000] != 2 {
		t.Fatal("recent height must be kept")
	}
	s.periods[10] = 1
	s.pruneBelowLocked(500) // below the retention distance: nothing to prune
	if s.periods[10] != 1 {
		t.Fatal("pruned inside the retention window")
	}
}

func TestRememberAnchor_Bounded(t *testing.T) {
	prev := anchorRecords
	anchorRecords = map[uint64]*anchorRecord{}
	t.Cleanup(func() { anchorRecords = prev })

	for p := uint64(1); p <= maxCachedAnchors+20; p++ {
		rememberAnchor(&anchorRecord{Period: p})
	}
	if n := len(anchorRecords); n != maxCachedAnchors {
		t.Fatalf("anchor cache holds %d, want %d", n, maxCachedAnchors)
	}
	if _, ok := anchorRecords[maxCachedAnchors+20]; !ok {
		t.Fatal("newest anchor evicted")
	}
	if _, ok := anchorRecords[1]; ok {
		t.Fatal("oldest anchor not evicted")
	}
}
