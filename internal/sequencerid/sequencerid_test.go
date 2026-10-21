package sequencerid

import (
	"testing"

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
