package seednode

import (
	"os"
	"testing"
)

func Test_GetPeer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: requires a reachable seed node (SEED_NODE_URL)")
	}
	PeerID := "12D3KooWSH54xa9zzgwbbpJTMtXWVEAQj518TshqTi84FMGCMT2C"
	seedNodeURL := os.Getenv("SEED_NODE_URL")
	if seedNodeURL == "" {
		t.Skip("integration: set SEED_NODE_URL (e.g. localhost:17002) to run")
	}
	client, err := NewClient(seedNodeURL)
	if err != nil {
		t.Fatalf("Failed to create seed node client: %v", err)
	}
	defer client.Close()
	Peer, err := client.GetPeer(PeerID)
	if err != nil {
		t.Fatalf("Failed to get peer: %v", err)
	}
	t.Logf("Peer: %v", Peer.Multiaddrs)
}
