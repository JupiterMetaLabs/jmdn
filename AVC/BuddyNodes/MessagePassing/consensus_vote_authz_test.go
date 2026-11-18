package MessagePassing

import (
	"crypto/rand"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/internal/sequencerid"
)

func newTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Only the sequencer may request this node's signed vote result. With no
// sequencer known, or an invalid one, nobody may. No config is loaded here, so
// there is no config pin and sequencerid uses the seed-snapshot value.
func TestVoteRequesterAuthorized(t *testing.T) {
	// sequencerid ignores older epochs, so each case uses a higher one.
	t.Cleanup(func() { sequencerid.SetFromSeed("", 1<<62) })
	seq := newTestPeerID(t)
	member := newTestPeerID(t)

	cases := []struct {
		name      string
		sequencer string
		allowed   []peer.ID
		refused   []peer.ID
	}{
		{"no sequencer known", "", nil, []peer.ID{seq, member}},
		{"invalid sequencer value", "not-a-peer-id", nil, []peer.ID{seq, member}},
		{"known sequencer", seq.String(), []peer.ID{seq}, []peer.ID{member, ""}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sequencerid.SetFromSeed(tc.sequencer, 1<<60+uint64(i))
			for _, p := range tc.allowed {
				if !voteRequesterAuthorized(p) {
					t.Fatalf("%s must be authorized", p)
				}
			}
			for _, p := range tc.refused {
				if voteRequesterAuthorized(p) {
					t.Fatalf("%q must be refused", p)
				}
			}
		})
	}
}
