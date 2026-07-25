package messaging

import (
	"testing"

	"gossipnode/config"
)

// The gossip publisher is injected (SetBlockGossipPublish) and gated by
// EnableBlockGossip. Verify the flag/nil-safety without needing pubsub.
func TestPublishBlockGossip_Injection(t *testing.T) {
	origFn := blockGossipPublish
	origEnabled := EnableBlockGossip
	t.Cleanup(func() { blockGossipPublish = origFn; EnableBlockGossip = origEnabled })

	var calls int
	var last config.BlockMessage
	SetBlockGossipPublish(func(m config.BlockMessage) { calls++; last = m })

	// Enabled + wired → publisher called with the message.
	EnableBlockGossip = true
	PublishBlockGossip(config.BlockMessage{ID: "abc", Type: "zkblock"})
	if calls != 1 || last.ID != "abc" {
		t.Fatalf("expected publisher called once with ID=abc, got calls=%d id=%q", calls, last.ID)
	}

	// Disabled → no-op.
	EnableBlockGossip = false
	PublishBlockGossip(config.BlockMessage{ID: "def"})
	if calls != 1 {
		t.Fatalf("disabled gossip must not publish, calls=%d", calls)
	}

	// Enabled but no publisher wired → must not panic.
	EnableBlockGossip = true
	SetBlockGossipPublish(nil)
	PublishBlockGossip(config.BlockMessage{ID: "ghi"})
}
