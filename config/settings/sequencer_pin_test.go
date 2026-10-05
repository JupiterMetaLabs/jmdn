package settings

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testPeerID(t *testing.T) string {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("IDFromPrivateKey: %v", err)
	}
	return id.String()
}

// TestSequencerPinEnvBinding verifies the pin is reachable through the
// JMDN_CONSENSUS_SEQUENCER_PINNED_PEER_ID env var. viper's AutomaticEnv only
// resolves keys it already knows, so this fails if the key is missing from
// setDefaults (the state PR #170 was in at cd680fc).
func TestSequencerPinEnvBinding(t *testing.T) {
	want := testPeerID(t)
	t.Setenv("JMDN_CONSENSUS_SEQUENCER_PINNED_PEER_ID", want)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Consensus.SequencerPinnedPeerID != want {
		t.Fatalf("pin not bound from env: got %q want %q", cfg.Consensus.SequencerPinnedPeerID, want)
	}
}

func TestSequencerPinDefaultEmpty(t *testing.T) {
	t.Setenv("JMDN_CONSENSUS_SEQUENCER_PINNED_PEER_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Consensus.SequencerPinnedPeerID != "" {
		t.Fatalf("expected empty default pin, got %q", cfg.Consensus.SequencerPinnedPeerID)
	}
}

// TestSequencerPinMalformedRefusesLoad verifies a typo in the pin is a startup
// error rather than a silent "drop every genuine L1 commit" at runtime.
func TestSequencerPinMalformedRefusesLoad(t *testing.T) {
	good := testPeerID(t)
	t.Setenv("JMDN_CONSENSUS_SEQUENCER_PINNED_PEER_ID", good[:len(good)-1]+"0")

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail on a malformed sequencer pin")
	}
}
