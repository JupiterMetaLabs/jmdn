package PubSubConnector

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
)

func controlTestPeer(t *testing.T) peer.ID {
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

// H5 (2026-10-22 audit): sequencer-only control messages arriving by gossip
// are acted on only when gossipsub signed them and the publisher is the
// sequencer. Ordinary messages are unaffected.
func TestSequencerControlAllowed(t *testing.T) {
	if !settings.IsLoaded() {
		if _, err := settings.Load(); err != nil {
			t.Fatal(err)
		}
	}
	c := settings.Get()
	prevPin := c.Consensus.SequencerPinnedPeerID
	seq, attacker := controlTestPeer(t), controlTestPeer(t)
	c.Consensus.SequencerPinnedPeerID = seq.String()
	t.Cleanup(func() { c.Consensus.SequencerPinnedPeerID = prevPin })

	msg := func(stage string, sender peer.ID, authed bool) *AVCStruct.GossipMessage {
		return &AVCStruct.GossipMessage{Sender: sender, SenderAuthenticated: authed,
			Data: &AVCStruct.Message{ACK: &AVCStruct.ACK{Stage: stage}}}
	}
	for _, stage := range []string{config.Type_BFTRequest, config.Type_AskForSubscription} {
		if sequencerControlAllowed(msg(stage, attacker, true)) {
			t.Fatalf("%s from another peer was allowed", stage)
		}
		if sequencerControlAllowed(msg(stage, seq, false)) {
			t.Fatalf("%s with an unauthenticated publisher was allowed", stage)
		}
		if !sequencerControlAllowed(msg(stage, seq, true)) {
			t.Fatalf("%s from the sequencer was refused", stage)
		}
	}
	if !sequencerControlAllowed(msg(config.Type_Publish, attacker, true)) {
		t.Fatal("an ordinary message must not be affected")
	}
}
