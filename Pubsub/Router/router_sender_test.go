package Router

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/internal/sequencerid"
)

func routerTestPeer(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// H5 (2026-10-22 audit): ASK_FOR_SUBSCRIPTION and END_PUBSUB reaching the
// Router from anyone but the sequencer must be dropped before any service
// runs. This test has no pubsub node, so a message that passes the check
// fails later with "PubSub node not properly initialized"; a dropped one
// returns nil.
func TestRouter_SubscriptionControlOnlyFromSequencer(t *testing.T) {
	if !settings.IsLoaded() {
		if _, err := settings.Load(); err != nil {
			t.Fatal(err)
		}
	}
	c := settings.Get()
	prevPin := c.Consensus.SequencerPinnedPeerID
	seq, attacker := routerTestPeer(t), routerTestPeer(t)
	c.Consensus.SequencerPinnedPeerID = seq.String()
	t.Cleanup(func() { c.Consensus.SequencerPinnedPeerID = prevPin })
	if sequencerid.Get() != seq.String() {
		t.Fatal("setup: the config pin should be the sequencer")
	}

	msg := func(stage string, sender peer.ID, authed bool) *AVCStruct.GossipMessage {
		return &AVCStruct.GossipMessage{Sender: sender, SenderAuthenticated: authed,
			Data: &AVCStruct.Message{ACK: &AVCStruct.ACK{Stage: stage}}}
	}
	for _, stage := range []string{config.Type_EndPubSub, config.Type_AskForSubscription} {
		if err := Router(msg(stage, attacker, true)); err != nil {
			t.Fatalf("%s from another peer must be dropped, got %v", stage, err)
		}
		if err := Router(msg(stage, seq, false)); err != nil {
			t.Fatalf("%s with an unauthenticated sender must be dropped, got %v", stage, err)
		}
		if err := Router(msg(stage, seq, true)); err == nil || !strings.Contains(err.Error(), "not properly initialized") {
			t.Fatalf("%s from the sequencer must reach the services, got %v", stage, err)
		}
	}
}
