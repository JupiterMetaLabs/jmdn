package Sequencer

import (
	"reflect"
	"testing"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/internal/reputation"

	"github.com/libp2p/go-libp2p/core/peer"
)

func buddyMapFor(t *testing.T, ids []peer.ID) map[int]PubSubMessages.Buddy_PeerMultiaddr {
	t.Helper()
	return PubSubMessages.ConvertBuddiesIntoHashMap_PeerMultiaddr(candidatesFor(t, ids))
}

func idStrings(ids []peer.ID) []string {
	out := make([]string, 0, len(ids))
	for _, p := range ids {
		out = append(out, p.String())
	}
	return out
}

// TestReputationCommittee_FillersAreNotClassified models the v2 round shape
// behind the false-Absent loop: 4 seats reachable and on the wire list, 3
// unseated fillers completing MainPeers. The round fails and no filler voted
// (each skipped the vote trigger as "not in the buddy list"). Before the fix
// all 3 fillers are charged Absent; after it, none is, while a seated peer
// that stayed silent still is.
func TestReputationCommittee_FillersAreNotClassified(t *testing.T) {
	ids := testPeers(t, 7)
	seatsOnWire, fillers := ids[:4], ids[4:]
	mainPeers := append(append([]peer.ID(nil), seatsOnWire...), fillers...)
	wire := buddyMapFor(t, seatsOnWire)

	// Seats 0-2 voted YES, seat 3 was silent; fillers never voted.
	votes := map[string]bool{ids[0].String(): true, ids[1].String(): true, ids[2].String(): true}

	before := reputation.ClassifyRound(idStrings(mainPeers), votes, false)
	for _, f := range fillers {
		if before[f.String()] != reputation.Absent {
			t.Fatalf("precondition: filler %s should be Absent under the old committee, got %q", f, before[f.String()])
		}
	}

	committee := ReputationCommittee(mainPeers, wire)
	if want := idStrings(seatsOnWire); !reflect.DeepEqual(committee, want) {
		t.Fatalf("committee = %v, want %v", committee, want)
	}
	after := reputation.ClassifyRound(committee, votes, false)
	for _, f := range fillers {
		if ev, classified := after[f.String()]; classified {
			t.Fatalf("filler %s was classified %q; it was never asked to vote", f, ev)
		}
	}
	if after[ids[3].String()] != reputation.Absent {
		t.Fatalf("silent seated peer on the wire list must still be Absent, got %q", after[ids[3].String()])
	}
	if after[ids[0].String()] != reputation.MinorityDissent {
		t.Fatalf("YES on a failed round is zero-delta dissent, got %q", after[ids[0].String()])
	}
}

func TestReputationCommittee_WireOnlyPeerIsNotClassified(t *testing.T) {
	// On the wire list but not a MainPeer (e.g. refused subscription): the
	// sequencer never requested its result, so it is not classified.
	ids := testPeers(t, 3)
	got := ReputationCommittee(ids[:2], buddyMapFor(t, ids))
	if want := idStrings(ids[:2]); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReputationCommittee_EmptyWireListClassifiesNobody(t *testing.T) {
	// The stall shape: dialable 0, so the buddy list is empty.
	ids := testPeers(t, 7)
	for _, wire := range []map[int]PubSubMessages.Buddy_PeerMultiaddr{nil, {}} {
		if got := ReputationCommittee(ids, wire); len(got) != 0 {
			t.Fatalf("empty wire list must classify nobody, got %v", got)
		}
	}
}

func TestReputationCommittee_KeepsMainPeersOrderAndDedupes(t *testing.T) {
	ids := testPeers(t, 4)
	mainPeers := []peer.ID{ids[3], ids[1], ids[3], ids[0]}
	got := ReputationCommittee(mainPeers, buddyMapFor(t, []peer.ID{ids[0], ids[1], ids[3]}))
	if want := idStrings([]peer.ID{ids[3], ids[1], ids[0]}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReputationCommittee_ReadsBuddyListLikeReceivers(t *testing.T) {
	// Receivers only treat keys 0..MaxMainPeers-1 as buddies; an entry beyond
	// that is not on the wire list for them, so it is not classified here.
	ids := testPeers(t, config.MaxMainPeers+1)
	wire := buddyMapFor(t, ids)
	extra := ids[config.MaxMainPeers]
	got := ReputationCommittee([]peer.ID{extra}, wire)
	if len(got) != 0 {
		t.Fatalf("peer at buddy index %d must not be classified, got %v", config.MaxMainPeers, got)
	}
}
