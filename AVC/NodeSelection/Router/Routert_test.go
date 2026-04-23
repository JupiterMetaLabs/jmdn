package Router

import (
	"fmt"
	"testing"
)

func TestGetBuddyNodes(t *testing.T) {
	router := NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(1)
	if err != nil {
		t.Fatalf("Failed to get buddies: %v", err)
	}
	fmt.Println(buddies)
}
