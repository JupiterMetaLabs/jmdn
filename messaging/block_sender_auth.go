package messaging

import (
	"context"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/internal/l1auth"
	"gossipnode/metrics"
)

// blockSenderPin returns the pinned sequencer peer ID. A variable so tests can
// set it without a config file.
var blockSenderPin = pinnedSequencerID

// AuthorizeBlockSender reports whether a finalized block delivered by the
// transport-authenticated peer `authenticated` may be admitted. Same contract
// as l1finality.AuthorizeGossipSender: with a pinned sequencer (required under
// production posture) only that peer is accepted (enforced=true); with no pin
// every sender is accepted and the caller WARNs (enforced=false).
//
// `authenticated` must be the transport identity — the libp2p stream peer, or
// the gossipsub publisher (msg.GetFrom(), signed under StrictSign) — never a
// payload field.
func AuthorizeBlockSender(authenticated string) (ok, enforced bool) {
	pin := blockSenderPin()
	if !l1auth.Enforced(pin) {
		return true, false
	}
	return l1auth.IsSequencer(authenticated, pin), true
}

// HandleGossipedBlockMessageFrom applies a block received on the block gossip
// topic after checking that its gossipsub publisher is the pinned sequencer.
// Gossip relays preserve the original publisher and its signature, so this
// holds however many hops the message travelled.
func HandleGossipedBlockMessageFrom(msg config.BlockMessage, publisher peer.ID) {
	ok, enforced := AuthorizeBlockSender(publisher.String())
	if !ok {
		metrics.BlocksRejectedCounter.WithLabelValues("publisher_not_sequencer", "gossip").Inc()
		broadcastLogger().Warn(context.Background(), "Dropping gossiped block not published by the pinned sequencer",
			ion.String("publisher", publisher.String()))
		return
	}
	if !enforced {
		broadcastLogger().Debug(context.Background(), "Gossiped block publisher not authenticated: consensus.sequencer_pinned_peer_id is unset",
			ion.String("publisher", publisher.String()))
	}
	HandleGossipedBlockMessage(msg, "gossip:"+publisher.String())
}
