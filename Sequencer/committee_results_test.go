package Sequencer

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
)

func mustPeerID(t *testing.T, s string) peer.ID {
	t.Helper()
	id, err := peer.Decode(s)
	if err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return id
}

// Only the round's committee is asked for a signed vote result — those
// signatures form the CommitteeCertificate and the reward set. Validators that
// merely voted are never queried; the sequencer itself is excluded.
func TestCommitteePeerIDsForRound_CommitteeOnly(t *testing.T) {
	a := mustPeerID(t, "12D3KooWEyoppNCUx8Yx66oV9fJnriXwCcXwDDUA2kj6vnc6iDEp")
	b := mustPeerID(t, "12D3KooWHHzSeKaY8xuZVzkLbKFfvNgPPeKhFBGrMbNzbm5akpqu")
	self := mustPeerID(t, "12D3KooWJWoaqZhDaoEFshF7Rh1bpY9ohihFhzcW6d69Lr2NASuq")
	voterOnly := mustPeerID(t, "12D3KooWRBy97UB99e3J6hiPesre1MZeuNQvfan4gBziswrRJsNK")

	cm := PubSubMessages.NewConsensusMessageBuilder(nil).
		SetZKBlock(&config.ZKBlock{BlockNumber: 5}).
		SetBuddies([]PubSubMessages.Buddy_PeerMultiaddr{{PeerID: a}, {PeerID: b}, {PeerID: self}, {PeerID: a}})
	listener := &PubSubMessages.BuddyNode{}
	listener.BuddyNodes.Buddies_Nodes = []peer.ID{voterOnly} // must be ignored when the message carries a committee

	got := committeePeerIDsForRound(cm, listener, self)
	set := map[peer.ID]bool{}
	for _, p := range got {
		set[p] = true
	}
	if len(got) != 2 || !set[a] || !set[b] {
		t.Fatalf("got %v, want {a, b} (deduped, self excluded, no non-committee voters)", got)
	}

	// No committee on the message → the listener's committee list.
	empty := PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(&config.ZKBlock{BlockNumber: 5})
	got = committeePeerIDsForRound(empty, listener, self)
	if len(got) != 1 || got[0] != voterOnly {
		t.Fatalf("fallback: got %v", got)
	}
}
