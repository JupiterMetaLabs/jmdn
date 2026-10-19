package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPeerLabel_BoundsSeries(t *testing.T) {
	MessagesReceivedCounter.Reset()
	for i := 0; i < maxPeerLabels*4; i++ {
		MessagesReceivedCounter.WithLabelValues("block", PeerLabel(fmt.Sprintf("peer-%d", i))).Inc()
	}
	if n := testutil.CollectAndCount(MessagesReceivedCounter); n > maxPeerLabels {
		t.Fatalf("%d series after %d distinct peers, want <= %d", n, maxPeerLabels*4, maxPeerLabels)
	}
	// The most recent peer keeps its own label.
	last := fmt.Sprintf("peer-%d", maxPeerLabels*4-1)
	if v := testutil.ToFloat64(MessagesReceivedCounter.WithLabelValues("block", last)); v != 1 {
		t.Fatalf("recent peer series = %v, want 1", v)
	}
}

func TestProtocolLabel(t *testing.T) {
	if ProtocolLabel("zkblock") != "zkblock" || ProtocolLabel("timeout_vote") != "timeout_vote" {
		t.Fatal("known types must pass through")
	}
	if ProtocolLabel("attacker-chosen-123") != OtherLabel || ProtocolLabel("") != OtherLabel {
		t.Fatal("unknown types must collapse to other")
	}
}
