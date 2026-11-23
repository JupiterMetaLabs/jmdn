package MessagePassing

import (
	"sync"
	"time"

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
//
// Without a config pin the sequencer comes from the seed snapshot, which is
// refreshed as a side effect of other reads (for example ballot ingest). Those
// reads may not have run yet (a freshly started node) or may not reach the
// seed (committee anchoring takes the voter pool from the chain). So when the
// requester does not match, refresh the snapshot once and check again. The
// refresh only updates the known sequencer; it never authorizes on its own.
func voteRequesterAuthorized(remote peer.ID) bool {
	if remote == "" {
		return false
	}
	if requesterIsSequencer(remote) {
		return true
	}
	if sequencerid.ConfigPinned() || !refreshSequencerAllowed() {
		return false
	}
	refreshSequencer()
	return requesterIsSequencer(remote)
}

func requesterIsSequencer(remote peer.ID) bool {
	raw := sequencerid.Get() // config pin if set, else the seed-signed snapshot
	if raw == "" || raw == sequencerid.Unknown {
		return false
	}
	seq, err := peer.Decode(raw)
	return err == nil && remote == seq
}

// sequencerRefreshMinInterval bounds how often unmatched requests may refresh
// the seed snapshot, so peers that open request streams cannot make this node
// query the seed on every stream.
const sequencerRefreshMinInterval = 5 * time.Second

var (
	sequencerRefreshMu   sync.Mutex
	sequencerRefreshFn   func()
	sequencerRefreshLast time.Time
	sequencerRefreshNow  = time.Now
)

// SetSequencerRefresh wires the function that re-reads the seed committee
// snapshot (which records the sequencer via sequencerid.SetFromSeed). Call
// once at startup. Without it, an unmatched requester is refused at once.
func SetSequencerRefresh(fn func()) {
	sequencerRefreshMu.Lock()
	sequencerRefreshFn = fn
	sequencerRefreshMu.Unlock()
}

// refreshSequencerAllowed reports whether a refresh is wired and the last one
// was at least sequencerRefreshMinInterval ago, and if so claims this slot.
func refreshSequencerAllowed() bool {
	sequencerRefreshMu.Lock()
	defer sequencerRefreshMu.Unlock()
	if sequencerRefreshFn == nil {
		return false
	}
	now := sequencerRefreshNow()
	if !sequencerRefreshLast.IsZero() && now.Sub(sequencerRefreshLast) < sequencerRefreshMinInterval {
		return false
	}
	sequencerRefreshLast = now
	return true
}

func refreshSequencer() {
	sequencerRefreshMu.Lock()
	fn := sequencerRefreshFn
	sequencerRefreshMu.Unlock()
	if fn != nil {
		fn()
	}
}
