package MessagePassing

import (
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/internal/sequencerid"
)

// Vote-result requester authorization (D-26(d) / THEBE-AUDIT-HLD.md CON-03).
//
// A vote-result request is the sequencer asking a committee member to return,
// and BLS-sign, this node's aggregated vote for a specific block. Only the
// sequencer collects vote results (Sequencer CollectVoteResultsFromBuddies),
// so only the sequencer may ask. A committee member that could ask would
// obtain this node's signature over a block it chose from the proposal cache,
// and such signatures, pooled from a round that missed quorum, would certify a
// block the network never committed.
//
// The libp2p stream's remote peer ID is authenticated by the transport
// handshake, so comparing it with the sequencer's peer ID is sound. The
// sequencer is resolved on every call by internal/sequencerid: the config pin
// (consensus.sequencer_pinned_peer_id) if set, otherwise the sequencer peer ID
// in the seed-signed committee snapshot.
//
// The gate is always enforced: no flag, no fail-open branch. With no valid
// sequencer known the node signs nothing until it learns one. A refused
// request costs the requester one missed signature for one round, which BFT
// quorum already tolerates. The legacy buddy-to-buddy request path
// (TriggerForBFTFromSequencer) runs only with features.use_legacy_bft on and
// is refused by this gate.

// voteRequesterAuthorized reports whether remote, the authenticated stream
// peer, may request this node's signed vote result. With no valid sequencer
// known, nobody may: the node signs nothing until it learns the sequencer.
func voteRequesterAuthorized(remote peer.ID) bool {
	if remote == "" {
		return false
	}
	raw := sequencerid.Get() // config pin if set, else the seed-signed snapshot
	if raw == "" || raw == sequencerid.Unknown {
		return false
	}
	seq, err := peer.Decode(raw)
	return err == nil && remote == seq
}
