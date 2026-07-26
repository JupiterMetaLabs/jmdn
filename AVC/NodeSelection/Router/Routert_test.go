package Router

import (
	"fmt"
	"testing"
)

func TestGetBuddyNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("excluded from -short: requires full node settings / VRF material")
	}
	router := NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(1)
	if err != nil {
		t.Fatalf("Failed to get buddies: %v", err)
	}
	fmt.Println(buddies)
}
