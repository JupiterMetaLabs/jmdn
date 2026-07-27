package messaging

import (
	"gossipnode/config"
	"gossipnode/config/settings"
)

// Gossip fan-out of finalized blocks.
//
// The sequencer's finalized, committee-certified block is broadcast over direct
// libp2p streams to its connected peers (the committee) and hop-forwarded. On a
// large fleet that leaves non-committee validators dependent on catch-up. This
// file adds an ADDITIVE second path: publish the same block message to a
// persistent gossip topic (config.PubSub_BlockPropagation) that every node
// subscribes to, so delivery is epidemic and does not depend on a direct
// sequencer connection. Receivers run the identical fail-closed admitZKBlock
// certificate gate (via HandleReceivedBlockMessage), so signed-block propagation
// is preserved. The pubsub plumbing lives in package main; this package only
// exposes the injection points, so messaging need not import the Pubsub packages
// (avoiding an import cycle).

// EnableBlockGossip controls the gossip fan-out. Default ON; set
// JMDN_BLOCK_GOSSIP=0 to disable (the existing direct-stream broadcast still
// runs, so disabling is safe).
var EnableBlockGossip = envOn("JMDN_BLOCK_GOSSIP", true)

// directBlockPropagationEnv is the env override for direct (p2p) block
// propagation, evaluated at init for tooling/tests that never Load() settings.
var directBlockPropagationEnv = envOn("JMDN_DIRECT_BLOCK_PROPAGATION", false)

// directBlockPropagationEnabled reports whether finalized blocks are ALSO sent
// over direct per-peer libp2p streams IN ADDITION to the gossip mesh. Default is
// ENABLED (consensus.p2p defaults to 1) — direct + gossip both run, which is the
// resilient choice since it does not depend solely on gossip-mesh reachability;
// dedup + the per-block-hash apply lock make the double delivery safe. It is
// disabled (gossip-only) only when consensus.p2p is explicitly set to 0. Also
// forced on by the JMDN_DIRECT_BLOCK_PROPAGATION=1 env override (which cannot be
// used to turn it off — use consensus.p2p: 0 for that). When gossip is disabled
// the caller runs direct regardless, so a block is never left with no path.
func directBlockPropagationEnabled() bool {
	if settings.IsLoaded() && settings.Get().Consensus.P2P >= 1 {
		return true
	}
	return directBlockPropagationEnv
}

// blockGossipPublish is the injected publisher (wired in package main). nil => a
// no-op, so a node that never wires it simply does not gossip.
var blockGossipPublish func(config.BlockMessage)

// SetBlockGossipPublish wires the finalized-block gossip publisher. Call once at
// startup. Passing nil disables publishing.
func SetBlockGossipPublish(fn func(config.BlockMessage)) { blockGossipPublish = fn }

// PublishBlockGossip fans a finalized block message out over the gossip mesh, in
// addition to the direct-stream broadcast. No-op when disabled or unwired. Called
// from the sequencer's BroadcastBlockToEveryNodeWithExtraData path only.
func PublishBlockGossip(msg config.BlockMessage) {
	if !EnableBlockGossip {
		return
	}
	if fn := blockGossipPublish; fn != nil {
		fn(msg)
	}
}

// HandleGossipedBlockMessage runs a block received over the gossip topic through
// the same fail-closed validate-and-apply path as the direct stream. forward is
// false: the pubsub mesh already re-propagates, so no direct re-flood is needed.
func HandleGossipedBlockMessage(msg config.BlockMessage, sender string) {
	HandleReceivedBlockMessage(msg, sender, false)
}
