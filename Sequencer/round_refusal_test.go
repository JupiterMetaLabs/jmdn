package Sequencer

import (
	"context"
	"testing"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/messaging"

	"github.com/libp2p/go-libp2p/core/peer"
)

func withFreshProposerPeriods(t *testing.T) {
	t.Helper()
	saved := messaging.DefaultProposerPeriods
	messaging.DefaultProposerPeriods = messaging.NewProposerPeriods()
	t.Cleanup(func() { messaging.DefaultProposerPeriods = saved })
}

func consensusAtHeight(height uint64) *Consensus {
	msg := PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(&config.ZKBlock{BlockNumber: height})
	return &Consensus{ZKBlockData: msg}
}

// A restarted sequencer re-proposes height 40 at Period 0. Buddies that
// already signed Period 3 refuse with highest_period; once the round fails,
// the next proposal resumes at Period 4 instead of climbing one round at a time.
func TestRoundRefusals_ResumeAboveSignedPeriods(t *testing.T) {
	withFreshProposerPeriods(t)
	c := consensusAtHeight(40)
	for _, id := range []string{"12D3KooWA", "12D3KooWB", "12D3KooWC"} {
		c.noteRoundRefusal(`{"error":"round refused: stale_period","vote_result":0,"highest_period":3}`, peer.ID(id))
	}
	c.advancePeriodAfterFailedRound(context.Background(), 40, "test")
	if got := messaging.DefaultProposerPeriods.PeriodFor(40); got != 4 {
		t.Fatalf("next period %d, want 4", got)
	}
}

// Ordinary vote results and unrelated errors carry no hint and change nothing.
func TestRoundRefusals_OtherResponsesAreIgnored(t *testing.T) {
	withFreshProposerPeriods(t)
	c := consensusAtHeight(40)
	for _, resp := range []string{
		`{"error":"unknown block","vote_result":0}`,
		`not json`,
		``,
	} {
		for _, id := range []string{"12D3KooWA", "12D3KooWB", "12D3KooWC"} {
			c.noteRoundRefusal(resp, peer.ID(id))
		}
	}
	c.advancePeriodAfterFailedRound(context.Background(), 40, "test")
	if got := messaging.DefaultProposerPeriods.PeriodFor(40); got != 1 {
		t.Fatalf("next period %d, want 1 (plain increment)", got)
	}
}

func TestRoundRefusals_NoBlockInFlightIsANoOp(t *testing.T) {
	withFreshProposerPeriods(t)
	(&Consensus{}).noteRoundRefusal(`{"highest_period":9}`, peer.ID("12D3KooWA"))
}
