package Pubsub

import (
	"testing"
	"time"

	"gossipnode/config/PubSubMessages"
)

func TestCheckGossipEnvelope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := &PubSubMessages.GossipMessage{ID: "x", Topic: "t", Timestamp: now.Unix(), TTL: 1_000_000}
	if r := checkGossipEnvelope(m, now); r != "" || m.TTL != gossipMaxTTL {
		t.Fatalf("reason=%q ttl=%d, want clamp to %d", r, m.TTL, gossipMaxTTL)
	}
	if r := checkGossipEnvelope(&PubSubMessages.GossipMessage{TTL: -1}, now); r != "negative_ttl" {
		t.Fatalf("negative ttl: %q", r)
	}
	old := &PubSubMessages.GossipMessage{TTL: 5, Timestamp: now.Add(-gossipMaxAge - time.Second).Unix()}
	if r := checkGossipEnvelope(old, now); r != "stale" {
		t.Fatalf("stale: %q", r)
	}
	fut := &PubSubMessages.GossipMessage{TTL: 5, Timestamp: now.Add(gossipMaxFuture + time.Second).Unix()}
	if r := checkGossipEnvelope(fut, now); r != "future_timestamp" {
		t.Fatalf("future: %q", r)
	}
	// Direct messages carry no relay budget; their timestamp is not policed.
	if r := checkGossipEnvelope(&PubSubMessages.GossipMessage{TTL: 0}, now); r != "" {
		t.Fatalf("direct: %q", r)
	}
}

// Re-sending one payload under a new ID must map to the same content key,
// while a different payload must not.
func TestGossipContentKey(t *testing.T) {
	data := &PubSubMessages.Message{Message: "hello"}
	a := &PubSubMessages.GossipMessage{ID: "1", Topic: "t", Sender: "p", Timestamp: 5, TTL: 3, Data: data}
	b := *a
	b.ID, b.TTL = "2", 2
	ka, _ := gossipContentKey(a)
	kb, _ := gossipContentKey(&b)
	if ka != kb {
		t.Fatal("same payload under new ID/TTL must share a content key")
	}
	c := *a
	c.Data = &PubSubMessages.Message{Message: "other"}
	kc, _ := gossipContentKey(&c)
	if kc == ka {
		t.Fatal("different payload must differ")
	}
}
