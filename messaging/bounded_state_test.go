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
