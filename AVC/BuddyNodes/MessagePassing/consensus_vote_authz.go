package MessagePassing

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
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
// seed (committee anchoring takes the voter pool from the chain). So when no
// valid sequencer is known yet, refresh the snapshot once and check again. A
// requester that only fails to match a known sequencer triggers no refresh,
// so other peers cannot make this node query the seed. The refresh only
// updates the known sequencer; it never authorizes on its own.
func voteRequesterAuthorized(remote peer.ID) bool {
	if remote == "" {
		return false
	}
	if requesterIsSequencer(remote) {
		return true
	}
	if sequencerid.ConfigPinned() || noUsableSequencerReason() == "" || !refreshSequencerAllowed() {
		noteNoUsableSequencer()
		return false
	}
	refreshSequencer()
	if requesterIsSequencer(remote) {
		return true
	}
	noteNoUsableSequencer()
	return false
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

// noUsableSequencerReason returns why no requester can be authorized right
// now, or "" when a valid sequencer is known (the requester simply did not
// match it, which the request handler already logs).
func noUsableSequencerReason() string {
	raw := sequencerid.Get()
	switch {
	case raw == "" || raw == sequencerid.Unknown:
		return "no sequencer known: consensus.sequencer_pinned_peer_id is empty and the seed committee snapshot carries no sequencer peer ID"
	default:
		if _, err := peer.Decode(raw); err != nil {
			if sequencerid.ConfigPinned() {
				return "consensus.sequencer_pinned_peer_id is not a valid peer ID: " + err.Error()
			}
			return "the seed committee snapshot's sequencer peer ID is not valid: " + err.Error()
		}
	}
	return ""
}

// noUsableSequencerLogInterval bounds the "no usable sequencer" error log.
const noUsableSequencerLogInterval = time.Minute

var (
	noUsableSequencerMu   sync.Mutex
	noUsableSequencerLast time.Time
	noUsableSequencerLog  = func(reason string) {
		if l := logger(); l != nil {
			l.Error(context.Background(), "Vote-result requests are refused: this node cannot identify the sequencer and signs no vote result until it can",
				errors.New(reason),
				ion.String("function", "MessagePassing.voteRequesterAuthorized"))
		}
	}
)

// noteNoUsableSequencer logs, at most once per noUsableSequencerLogInterval,
// when requests are refused because no valid sequencer is known. Without it
// a node that can identify no sequencer refuses every request with only the
// per-request "not authorized" warning.
func noteNoUsableSequencer() {
	reason := noUsableSequencerReason()
	if reason == "" {
		return
	}
	noUsableSequencerMu.Lock()
	now := sequencerRefreshNow()
	if !noUsableSequencerLast.IsZero() && now.Sub(noUsableSequencerLast) < noUsableSequencerLogInterval {
		noUsableSequencerMu.Unlock()
		return
	}
	noUsableSequencerLast = now
	noUsableSequencerMu.Unlock()
	noUsableSequencerLog(reason)
}
