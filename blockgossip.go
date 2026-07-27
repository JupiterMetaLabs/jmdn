package main

import (
	"context"
	"encoding/json"
	"strings"

	"gossipnode/Pubsub"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/messaging"

	Publisher "gossipnode/Pubsub/Publish"
	Subscriber "gossipnode/Pubsub/Subscription"

	"github.com/rs/zerolog/log"
)

// startBlockGossip wires the additive finalized-block gossip fan-out on this node.
//
// It (1) injects the publisher used by the sequencer's finalized-block broadcast
// (messaging.PublishBlockGossip) and (2) subscribes to PubSub_BlockPropagation so
// every node applies gossiped blocks through the SAME fail-closed admitZKBlock
// gate as the direct stream. This is what lets finalized blocks reach the whole
// fleet instead of only the sequencer's directly-connected committee.
//
// Safe on all nodes and additive: the existing direct-stream broadcast is
// unchanged, dedup collapses any overlap, and non-sequencer nodes never finalize
// a block so they never publish. No-op when disabled (JMDN_BLOCK_GOSSIP=0) or when
// pubsub is unavailable.
func startBlockGossip(ctx context.Context, gps *PubSubMessages.GossipPubSub) {
	if !messaging.EnableBlockGossip || gps == nil {
		return
	}

	// Register the block-propagation channel in this node's access map BEFORE
	// subscribing. Pubsub access control (CanSubscribe) denies any channel absent
	// from gps.ChannelAccess, and nothing else registers this topic — so without
	// this every node's subscribe is rejected ("access denied: not authorized to
	// subscribe") and gossip delivers nothing, leaving the direct stream as the
	// only path. Public: the whole fleet must receive finalized blocks; admission
	// stays fail-closed at admitZKBlock regardless of transport. Idempotent —
	// "already exists" is expected on restart/re-entry and is not an error.
	if err := Pubsub.CreateChannel(gps, config.PubSub_BlockPropagation, true, nil); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		log.Warn().Err(err).Str("topic", config.PubSub_BlockPropagation).
			Msg("[BlockGossip] could not register block-propagation channel; subscribe will be denied")
	}

	// Publisher — invoked from messaging.BroadcastBlockToEveryNodeWithExtraData,
	// which only the sequencer reaches on finalize.
	messaging.SetBlockGossipPublish(func(bm config.BlockMessage) {
		payload, err := json.Marshal(bm)
		if err != nil {
			log.Warn().Err(err).Msg("[BlockGossip] marshal finalized block failed; direct broadcast still applies")
			return
		}
		pm := &PubSubMessages.Message{
			Sender:  gps.Host.ID(),
			Message: string(payload),
			ACK:     PubSubMessages.NewACKBuilder().True_ACK_Message(gps.Host.ID(), config.Type_BlockPropagation),
		}
		if err := Publisher.Publish(ctx, gps, config.PubSub_BlockPropagation, pm, nil); err != nil {
			log.Warn().Err(err).Msg("[BlockGossip] publish failed (non-fatal; direct broadcast still applies)")
		}
	})

	// Subscriber — every node applies gossiped blocks via the shared, fail-closed
	// admitZKBlock gate. The sequencer receives its own published block but dedup
	// (already marked processed at broadcast time) drops it.
	handler := func(gm *PubSubMessages.GossipMessage) {
		if gm == nil || gm.Data == nil {
			return
		}
		var bm config.BlockMessage
		if err := json.Unmarshal([]byte(gm.Data.Message), &bm); err != nil {
			log.Debug().Err(err).Msg("[BlockGossip] unmarshal gossiped block failed")
			return
		}
		sender := "gossip"
		if s := gm.Sender.String(); s != "" {
			sender = "gossip:" + s
		}
		messaging.HandleGossipedBlockMessage(bm, sender)
	}
	if err := Subscriber.Subscribe(ctx, gps, config.PubSub_BlockPropagation, handler); err != nil {
		log.Error().Err(err).Msg("[BlockGossip] subscribe failed — gossip block propagation inactive on this node")
		return
	}
	log.Info().Str("topic", config.PubSub_BlockPropagation).Msg("[BlockGossip] finalized-block gossip wired (publish + subscribe)")
}
