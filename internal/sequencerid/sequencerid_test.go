package sequencerid

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config/settings"
)

func load(t *testing.T) *settings.NodeConfig {
	t.Helper()
	if !settings.IsLoaded() {
		if _, err := settings.Load(); err != nil {
			t.Fatal(err)
		}
	}
	c := settings.Get()
	prevPin, prevEnv, prevStrict := c.Consensus.SequencerPinnedPeerID, c.Network.Environment, c.Security.StrictPosture
	t.Cleanup(func() {
		c.Consensus.SequencerPinnedPeerID, c.Network.Environment, c.Security.StrictPosture = prevPin, prevEnv, prevStrict
		resetForTest()
	})
	resetForTest()
	return c
}

func TestGet_SeedValueUsedWhenNoConfigPin(t *testing.T) {
	c := load(t)
	c.Consensus.SequencerPinnedPeerID = ""
	c.Network.Environment, c.Security.StrictPosture = "testnet", false
	if Get() != "" {
		t.Fatal("nothing known on testnet must stay unenforced")
	}
	SetFromSeed("seq-A", 10)
	if Get() != "seq-A" {
		t.Fatalf("want seed value, got %q", Get())
	}
	SetFromSeed("seq-old", 9) // older epoch ignored
	if Get() != "seq-A" {
		t.Fatal("an older epoch must not replace the current value")
	}
}

func TestGet_ConfigPinWins(t *testing.T) {
	c := load(t)
	c.Consensus.SequencerPinnedPeerID = "seq-config"
	SetFromSeed("seq-seed", 10)
	if Get() != "seq-config" {
		t.Fatalf("config pin must win, got %q", Get())
	}
}

func TestGet_ProductionUnknownFailsClosed(t *testing.T) {
	c := load(t)
	c.Consensus.SequencerPinnedPeerID = ""
	c.Network.Environment = "mainnet"
	if Get() != Unknown {
		t.Fatalf("production with no sequencer must return Unknown, got %q", Get())
	}
}

// resetForTest clears the seed value. It lives in the test file because
// .golangci.yml sets run.tests: false, so a helper used only by tests but
// declared in the production file reads as dead code to the unused linter.
func resetForTest() {
	mu.Lock()
	seedID, seedEpoch = "", 0
	mu.Unlock()
}

func testPeerID(t *testing.T) string {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// H5 (2026-10-22 audit): sequencer-only control messages (BFT_REQUEST,
// ASK_FOR_SUBSCRIPTION, END_PUBSUB) pass only from the sequencer, and only
// when the sender identity was authenticated by the transport.
func TestAuthorizeSender(t *testing.T) {
	c := load(t)
	c.Consensus.SequencerPinnedPeerID = ""
	c.Network.Environment, c.Security.StrictPosture = "testnet", false
	seq, other := testPeerID(t), testPeerID(t)

	if ok, enforced := AuthorizeSender(other, false); !ok || enforced {
		t.Fatalf("no sequencer known on testnet: want accepted and unenforced, got ok=%v enforced=%v", ok, enforced)
	}

	SetFromSeed(seq, 10)
	for name, c := range map[string]struct {
		sender string
		authed bool
		want   bool
	}{
		"sequencer":                  {seq, true, true},
		"another peer":               {other, true, false},
		"sequencer, unauthenticated": {seq, false, false},
		"empty sender":               {"", true, false},
	} {
		ok, enforced := AuthorizeSender(c.sender, c.authed)
		if ok != c.want || !enforced {
			t.Fatalf("%s: got ok=%v enforced=%v, want ok=%v enforced=true", name, ok, enforced, c.want)
		}
	}
}

func TestAuthorizeSender_ProductionWithNoSequencerRefusesEveryone(t *testing.T) {
	c := load(t)
	c.Consensus.SequencerPinnedPeerID = ""
	c.Network.Environment = "mainnet"
	if ok, enforced := AuthorizeSender(testPeerID(t), true); ok || !enforced {
		t.Fatalf("production with no sequencer known must refuse, got ok=%v enforced=%v", ok, enforced)
	}
}
