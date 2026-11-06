package thebesync

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/messaging"
)

func newPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func legacySettings(main peer.ID, until uint64) settings.LegacyFollowSettings {
	return settings.LegacyFollowSettings{
		Enabled:         true,
		MainSequencer:   "/ip4/127.0.0.1/tcp/15000/p2p/" + main.String(),
		UntilHeight:     until,
		CatchUpInterval: time.Hour,
	}
}

// A legacy block fetched from any peer but the pinned main sequencer is
// refused before anything is applied.
func TestApplier_LegacyBlockFromOtherPeerRefused(t *testing.T) {
	main, other := newPeerID(t), newPeerID(t)
	defer messaging.OverrideLegacyFollowSettings(legacySettings(main, 0))()

	raw, _ := json.Marshal(&config.ZKBlock{BlockNumber: 5})
	for _, src := range []peer.ID{other, ""} {
		_, _, _, err := Applier{Source: src}.Apply(raw, 4, "0x0", false)
		if err == nil || !strings.Contains(err.Error(), "not the pinned main sequencer") {
			t.Fatalf("source %q: err = %v", src, err)
		}
	}
}

func TestNewLegacyFollower_RequiresPeerID(t *testing.T) {
	if _, err := NewLegacyFollower(nil, settings.LegacyFollowSettings{Enabled: true, MainSequencer: "/ip4/127.0.0.1/tcp/1"}); err == nil {
		t.Fatal("follower built from a pin without /p2p")
	}
	f, err := NewLegacyFollower(nil, legacySettings(newPeerID(t), 0))
	if err != nil {
		t.Fatal(err)
	}
	f.Nudge()
	f.Nudge() // never blocks
}

// Run catches up immediately, again on a nudge, and stops once the tip
// reaches until_height.
func TestLegacyFollower_RunNudgeAndStop(t *testing.T) {
	f, err := NewLegacyFollower(nil, legacySettings(newPeerID(t), 10))
	if err != nil {
		t.Fatal(err)
	}
	var tip, calls atomic.Uint64
	tip.Store(5)
	f.tipFn = func() (uint64, error) { return tip.Load(), nil }
	f.catchUp = func(context.Context) (uint64, error) {
		if calls.Add(1) == 2 {
			tip.Store(10) // the second pass reaches until_height
		}
		return tip.Load(), nil
	}
	done := make(chan struct{})
	go func() { f.Run(context.Background()); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.Nudge()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not stop at until_height")
	}
	if calls.Load() != 2 {
		t.Fatalf("catch-up ran %d times, want 2", calls.Load())
	}
}

// With until_height 0 the follower never stops on its own.
func TestLegacyFollower_OpenEndedStopsOnlyOnContext(t *testing.T) {
	f, err := NewLegacyFollower(nil, legacySettings(newPeerID(t), 0))
	if err != nil {
		t.Fatal(err)
	}
	f.tipFn = func() (uint64, error) { return 1 << 40, nil }
	f.catchUp = func(context.Context) (uint64, error) { return 0, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("open-ended follower stopped by itself")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("follower ignored context cancellation")
	}
}
