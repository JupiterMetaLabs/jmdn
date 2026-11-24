package messaging

import (
	"testing"
	"time"

	"gossipnode/config"
)

func envelopeMsg(sender, msgType string, hops int, ts int64) BroadcastMessageStruct {
	m := BroadcastMessageStruct{Sender: sender, Content: msgType + " broadcast", Timestamp: ts, Hops: hops, Type: msgType, Data: "payload"}
	m.ID = expectedBroadcastID(&m)
	return m
}

func TestCheckBroadcastEnvelope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := now.Unix()
	cases := []struct {
		name   string
		mutate func(*BroadcastMessageStruct)
		remote string
		want   string
	}{
		{"direct from origin", nil, "A", ""},
		{"relayed via other peer", func(m *BroadcastMessageStruct) { m.Hops = 2 }, "B", ""},
		{"negative hops", func(m *BroadcastMessageStruct) { m.Hops = -3 }, "A", "hops_out_of_range"},
		{"hops above max", func(m *BroadcastMessageStruct) { m.Hops = config.MaxHops + 1 }, "A", "hops_out_of_range"},
		{"forged id", func(m *BroadcastMessageStruct) { m.ID = "fresh-id-123" }, "A", "id_mismatch"},
		{"data changed under same id", func(m *BroadcastMessageStruct) { m.Data = "other" }, "A", "id_mismatch"},
		{"spoofed origin at hop 0", nil, "B", "sender_mismatch"},
		{"stale", func(m *BroadcastMessageStruct) {
			m.Timestamp = now.Add(-broadcastMaxAge - time.Second).Unix()
			m.ID = expectedBroadcastID(m)
		}, "A", "stale"},
		{"future", func(m *BroadcastMessageStruct) {
			m.Timestamp = now.Add(broadcastMaxFuture + time.Second).Unix()
			m.ID = expectedBroadcastID(m)
		}, "A", "future_timestamp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := envelopeMsg("A", "general", 0, ts)
			if c.mutate != nil {
				c.mutate(&m)
			}
			if got := checkBroadcastEnvelope(&m, c.remote, now); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The vote trigger's ID covers Content only (see BroadcastVoteTrigger); the
// check must accept what the sender actually produces.
func TestCheckBroadcastEnvelope_VoteTriggerID(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := BroadcastMessageStruct{Sender: "S", Content: "Vote trigger broadcast - initiate voting process",
		Timestamp: now.Unix(), Hops: config.MaxHops, Type: voteTriggerBroadcastType, Data: "{}"}
	m.ID = generateMessageID(m.Sender, m.Content, m.Timestamp)
	if got := checkBroadcastEnvelope(&m, "S", now); got != "" {
		t.Fatalf("vote trigger rejected: %s", got)
	}
}

// The allowlist held the three timeout-certificate types until 94e8fb6 (#196)
// removed that flow. It is empty now, so nothing is re-flooded — including the
// unrecognised types that used to be relayed to every peer. If a type is ever
// added to relayableBroadcastTypes, assert it here too.
func TestBroadcastRelayable(t *testing.T) {
	if len(relayableBroadcastTypes) != 0 {
		t.Fatalf("a relayable type was added without extending this test: %v", relayableBroadcastTypes)
	}
	for _, typ := range []string{"", "general", voteTriggerBroadcastType, "made_up", "timeout_vote", "timeout_cert"} {
		if broadcastRelayable(typ) {
			t.Errorf("%q must not be relayed", typ)
		}
	}
}
