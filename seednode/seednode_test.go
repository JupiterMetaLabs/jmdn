package seednode

import (

	"gossipnode/config"
	"os"
	"testing"
)

func Test_GetPeer(t *testing.T) {
	PeerID := "12D3KooWSH54xa9zzgwbbpJTMtXWVEAQj518TshqTi84FMGCMT2C"
	seedNodeURL := os.Getenv("SEED_NODE_URL")
	if seedNodeURL == "" {
		seedNodeURL = config.SeedNodeURL
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

