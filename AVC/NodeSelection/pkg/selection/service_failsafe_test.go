package selection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

// D-59 through the production path. GetBuddyNodesWithNodes is what
// GetBuddyNodes (and so the sequencer's warmup, via NodeselectionRouter) calls.
// It used to run the plain band filter and return ErrNoPeersAvailable as soon
// as that came back empty - before SelectMultipleBuddies' fail-safe could run.
// So a fleet whose reputation weights all fell below the floor still halted
// the chain, exactly as on 2026-09-17, even though the fail-safe existed.

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv
}

func TestGetBuddyNodesWithNodes_CollapsedFleetFallsBackInsteadOfHalting(t *testing.T) {
	fleet := collapsedFleet(5) // every peer active, every weight below the 0.50 floor

	got, err := GetBuddyNodesWithNodes(context.Background(), "self", testKey(t), []byte("salt"), fleet, 4096)
	if err != nil {
		t.Fatalf("collapsed fleet must fall back to the active set, got error: %v", err)
	}
	if len(got) != len(fleet) {
		t.Fatalf("want all %d active peers as candidates, got %d", len(fleet), len(got))
	}
}

func TestGetBuddyNodesWithNodes_FallbackStillExcludesSelfAndInactive(t *testing.T) {
	nodes := []Node{
		mkNode("self", true, 0.34),
		mkNode("peer-A", true, 0.34),
		mkNode("peer-B", false, 0.34),
		mkNode("peer-C", true, 0.34),
	}

	got, err := GetBuddyNodesWithNodes(context.Background(), "self", testKey(t), []byte("salt"), nodes, 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids := map[string]bool{}
	for _, b := range got {
		ids[b.Node.PeerId] = true
	}
	if len(ids) != 2 || !ids["peer-A"] || !ids["peer-C"] {
		t.Fatalf("fallback must be exactly the active non-self peers {peer-A, peer-C}, got %v", ids)
	}
}

func TestGetBuddyNodesWithNodes_NoActivePeersStillErrors(t *testing.T) {
	nodes := []Node{mkNode("self", true, 0.7), mkNode("peer-A", false, 0.7)}

	_, err := GetBuddyNodesWithNodes(context.Background(), "self", testKey(t), []byte("salt"), nodes, 4096)
	if !errors.Is(err, ErrNoPeersAvailable) {
		t.Fatalf("with no active non-self peer the fail-safe has nothing to offer; want ErrNoPeersAvailable, got %v", err)
	}
}

func TestGetBuddyNodesWithNodes_HealthyBandIsStillFiltered(t *testing.T) {
	// When the band is non-empty the fail-safe must not fire: below-floor
	// peers stay excluded, exactly as before.
	nodes := []Node{
		mkNode("peer-A", true, 0.70),
		mkNode("peer-B", true, 0.34),
		mkNode("peer-C", true, 0.80),
	}

	got, err := GetBuddyNodesWithNodes(context.Background(), "self", testKey(t), []byte("salt"), nodes, 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, b := range got {
		if b.Node.PeerId == "peer-B" {
			t.Fatalf("below-floor peer-B must stay excluded while the band is non-empty")
		}
	}
	if len(got) != 2 {
		t.Fatalf("want the 2 in-band peers, got %d", len(got))
	}
}
