package MessagePassing

import (
	"crypto/rand"
	"testing"
	"time"

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
	t.Cleanup(func() { sequencerid.SetFromSeed("", nextSeedEpoch()) })
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
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sequencerid.SetFromSeed(tc.sequencer, nextSeedEpoch())
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

// sequencerid ignores an epoch lower than the last one recorded, so every
// SetFromSeed in these tests uses a fresh, higher epoch.
var seedEpochCounter uint64 = 1 << 63

func nextSeedEpoch() uint64 { seedEpochCounter++; return seedEpochCounter }

// installSequencerRefresh wires fn as the seed refresh, with a controllable
// clock, and restores the previous state afterwards.
func installSequencerRefresh(t *testing.T, fn func()) *time.Time {
	t.Helper()
	sequencerRefreshMu.Lock()
	savedFn, savedLast, savedNow := sequencerRefreshFn, sequencerRefreshLast, sequencerRefreshNow
	sequencerRefreshMu.Unlock()
	now := time.Unix(1_000_000, 0)
	sequencerRefreshNow = func() time.Time { return now }
	sequencerRefreshLast = time.Time{}
	SetSequencerRefresh(fn)
	t.Cleanup(func() {
		sequencerRefreshMu.Lock()
		sequencerRefreshFn, sequencerRefreshLast, sequencerRefreshNow = savedFn, savedLast, savedNow
		sequencerRefreshMu.Unlock()
		sequencerid.SetFromSeed("", nextSeedEpoch())
	})
	return &now
}

// A node that has not read the seed snapshot yet learns the sequencer on the
// first request and authorizes it; a non-sequencer is still refused after the
// refresh.
func TestVoteRequesterRefreshesUnknownSequencer(t *testing.T) {
	seq := newTestPeerID(t)
	member := newTestPeerID(t)
	sequencerid.SetFromSeed("", nextSeedEpoch())
	refreshes := 0
	now := installSequencerRefresh(t, func() {
		refreshes++
		sequencerid.SetFromSeed(seq.String(), nextSeedEpoch())
	})

	if !voteRequesterAuthorized(seq) {
		t.Fatal("the sequencer must be authorized after the refresh")
	}
	if refreshes != 1 {
		t.Fatalf("want 1 refresh, got %d", refreshes)
	}
	*now = now.Add(time.Minute)
	if voteRequesterAuthorized(member) {
		t.Fatal("a non-sequencer must be refused after the refresh")
	}
}

// After the seed switches sequencers, the new sequencer is authorized once the
// refresh records it, and the previous one is refused.
func TestVoteRequesterRefreshFollowsSequencerChange(t *testing.T) {
	oldSeq := newTestPeerID(t)
	newSeq := newTestPeerID(t)
	sequencerid.SetFromSeed(oldSeq.String(), nextSeedEpoch())
	installSequencerRefresh(t, func() { sequencerid.SetFromSeed(newSeq.String(), nextSeedEpoch()) })

	if !voteRequesterAuthorized(newSeq) {
		t.Fatal("the new sequencer must be authorized after the refresh")
	}
	if voteRequesterAuthorized(oldSeq) {
		t.Fatal("the previous sequencer must be refused once the new one is recorded")
	}
}

// Unmatched requests refresh at most once per sequencerRefreshMinInterval, and
// a refresh that learns nothing still refuses.
func TestVoteRequesterRefreshIsRateLimited(t *testing.T) {
	stranger := newTestPeerID(t)
	sequencerid.SetFromSeed("", nextSeedEpoch())
	refreshes := 0
	now := installSequencerRefresh(t, func() { refreshes++ })

	for range 10 {
		if voteRequesterAuthorized(stranger) {
			t.Fatal("must refuse when no sequencer is known")
		}
	}
	if refreshes != 1 {
		t.Fatalf("want 1 refresh within the interval, got %d", refreshes)
	}
	*now = now.Add(sequencerRefreshMinInterval)
	_ = voteRequesterAuthorized(stranger)
	if refreshes != 2 {
		t.Fatalf("want a second refresh after the interval, got %d", refreshes)
	}
}

// With no refresh wired, an unmatched requester is refused at once.
func TestVoteRequesterNoRefreshWired(t *testing.T) {
	stranger := newTestPeerID(t)
	sequencerid.SetFromSeed("", nextSeedEpoch())
	installSequencerRefresh(t, nil)
	if voteRequesterAuthorized(stranger) {
		t.Fatal("must refuse with no sequencer and no refresh")
	}
}
