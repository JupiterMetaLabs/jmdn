package Router

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestGetBuddyNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("excluded from -short: requires full node settings / VRF material")
	}
	t.Setenv("JMDN_NODE_SELECTION_MNEMONIC", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	t.Setenv("JMDN_NETWORK_SALT", "test-salt")

	if err := os.MkdirAll("config", 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove("config/peer.json")
		_ = os.Remove("config")
	})

	dummyPeer := `{"peer_id":"12D3KooWDfAJSqixNF7p7Sqyez8KWFWYiFxWqGXpbQtMp39aNGbr","priv_key_b64":""}`
	if err := os.WriteFile("config/peer.json", []byte(dummyPeer), 0644); err != nil {
		t.Fatalf("write peer.json: %v", err)
	}

	router := NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(1)
	if err != nil {
		// A reachable seed node is an ENVIRONMENT prerequisite this test does
		// not set up: GetBuddyNodes dials whatever consensus/network config
		// names. Skipping keeps the rest of this package's suite meaningful —
		// a hard failure here left the package permanently red, so a genuine
		// regression in it could not be noticed.
		if strings.Contains(err.Error(), "no seednode URL configured") {
			t.Skip("no seednode configured in this environment — set the seednode URL to run this test")
		}
		t.Fatalf("Failed to get buddies: %v", err)
	}
	fmt.Println(buddies)
}
