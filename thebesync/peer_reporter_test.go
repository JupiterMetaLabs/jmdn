package thebesync

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Peers claiming a huge height must not become the sole catch-up source: every
// peer ahead of us is a target. Peers at or below the corroborated head come
// first, most advanced first; a claim above the head comes last.
func TestSelectCatchUpTargets(t *testing.T) {
	ids := func(s []peerHeadSample) []peer.ID {
		out := make([]peer.ID, len(s))
		for i, r := range s {
			out[i] = r.id
		}
		return out
	}
	cases := []struct {
		name     string
		ourTip   uint64
		samples  []peerHeadSample
		wantHead uint64
		wantIDs  []peer.ID
	}{
		{
			name:     "lone liar does not set the head and is tried last",
			ourTip:   95,
			samples:  []peerHeadSample{{"honest1", 100}, {"liar", 1_000_000}, {"honest2", 100}, {"behind", 90}},
			wantHead: 100,
			wantIDs:  []peer.ID{"honest1", "honest2", "liar"},
		},
		{
			// Two peers can corroborate any head; they are tried first, and a
			// CatchUp that serves nothing real fails over to the honest peers,
			// which stay in the list.
			name:     "two colluding liars set the head but honest peers stay in the list",
			ourTip:   100,
			samples:  []peerHeadSample{{"liarA", 1_000_000_000}, {"h1", 105}, {"liarB", 1_000_000_000}, {"h2", 110}, {"same", 100}},
			wantHead: 1_000_000_000,
			wantIDs:  []peer.ID{"liarA", "liarB", "h2", "h1"},
		},
		{
			name:     "peer below the head but above us is a target, after the head",
			ourTip:   100,
			samples:  []peerHeadSample{{"a", 120}, {"b", 120}, {"c", 103}, {"d", 99}},
			wantHead: 120,
			wantIDs:  []peer.ID{"a", "b", "c"},
		},
		{
			// A peer always answering tip+1 used to sort first and hold every
			// round to one block; it now goes after the corroborated head.
			name:     "drip peer at tip+1 does not set the pace",
			ourTip:   100,
			samples:  []peerHeadSample{{"h1", 120}, {"drip", 101}, {"h2", 120}},
			wantHead: 120,
			wantIDs:  []peer.ID{"h1", "h2", "drip"},
		},
		{
			name:     "lone claim above the head is tried after every corroborated peer",
			ourTip:   100,
			samples:  []peerHeadSample{{"low", 102}, {"liar", 900}, {"mid", 110}, {"top", 115}},
			wantHead: 115,
			wantIDs:  []peer.ID{"top", "mid", "low", "liar"},
		},
		{
			name:     "nobody ahead of us gives no targets",
			ourTip:   100,
			samples:  []peerHeadSample{{"a", 100}, {"b", 98}},
			wantHead: 98,
			wantIDs:  []peer.ID{},
		},
		{
			name:     "single sample is used as is",
			ourTip:   10,
			samples:  []peerHeadSample{{"only", 42}},
			wantHead: 42,
			wantIDs:  []peer.ID{"only"},
		},
	}
	for _, c := range cases {
		head, targets := selectCatchUpTargets(c.samples, c.ourTip)
		if head != c.wantHead {
			t.Errorf("%s: head = %d, want %d", c.name, head, c.wantHead)
		}
		got := ids(targets)
		if len(got) != len(c.wantIDs) {
			t.Errorf("%s: targets = %v, want %v", c.name, got, c.wantIDs)
			continue
		}
		for i := range got {
			if got[i] != c.wantIDs[i] {
				t.Errorf("%s: targets = %v, want %v", c.name, got, c.wantIDs)
				break
			}
		}
	}
}
