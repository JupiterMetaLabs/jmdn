package Sequencer

import (
	"context"
	"encoding/json"
	"testing"

	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/messaging"

	"github.com/libp2p/go-libp2p/core/peer"
)

// abstainWire builds the bytes a not-ready node sends: the message wrapper
// with the abstain JSON (including highest_period) as its inner message.
func abstainWire(t *testing.T, inner string) string {
	t.Helper()
	sender, err := peer.Decode("12D3KooWQXMLAiw4XL8VEz92vPJeWnNv8L5uZkHdjAUjsH2yj38s")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(PubSubMessages.Message{Sender: sender, Message: inner, Timestamp: 1})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// End to end on the sequencer side: three seated nodes are still starting up
// and abstain, but report that they voted at period 1. The failed round then
// resumes at period 2, not at the reused period 1 that caused the conflict.
func TestAbstainReplies_RaiseNextPeriod(t *testing.T) {
	withFreshProposerPeriods(t)
	c := consensusAtHeight(22464)
	inner := `{"error":"node not synced; abstaining from consensus","highest_period":1}`
	for _, id := range []string{"12D3KooWA", "12D3KooWB", "12D3KooWC"} {
		if r := c.parseVoteResultResponse(abstainWire(t, inner), peer.ID(id)); r != nil {
			t.Fatalf("abstain must not yield a BLS result, got %+v", r)
		}
	}
	c.advancePeriodAfterFailedRound(context.Background(), 22464, "test")
	if got := messaging.DefaultProposerPeriods.PeriodFor(22464); got != 2 {
		t.Fatalf("next period %d, want 2", got)
	}
}

// The old abstain reply (no highest_period) gives no hint: plain +1. This is
// the behaviour fix #2 replaces.
func TestAbstainReplies_WithoutHintArePlainIncrement(t *testing.T) {
	withFreshProposerPeriods(t)
	c := consensusAtHeight(22464)
	inner := `{"error":"node not synced; abstaining from consensus"}`
	for _, id := range []string{"12D3KooWA", "12D3KooWB", "12D3KooWC"} {
		c.parseVoteResultResponse(abstainWire(t, inner), peer.ID(id))
	}
	c.advancePeriodAfterFailedRound(context.Background(), 22464, "test")
	if got := messaging.DefaultProposerPeriods.PeriodFor(22464); got != 1 {
		t.Fatalf("next period %d, want 1", got)
	}
}
