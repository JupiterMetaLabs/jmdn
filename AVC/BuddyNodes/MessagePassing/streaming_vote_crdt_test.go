package MessagePassing

// Regression guard for the gap found while verifying Stage 2's spec against
// this repo (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md): node/node.go's own
// VoteCRDTLayer wiring only runs inside a nil-guarded fallback
// (`if Get_ForListner() == nil`) that never fires once NewListenerNode has
// already run — which it always has, since main.go/Consensus.go call it
// directly at startup. This test calls the REAL primary path, not the
// singleton getter alone, so a future refactor that drops the field from
// this literal fails here instead of silently shipping a nil VoteCRDTLayer
// on every real node.

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p"
)

func TestNewListenerNode_PopulatesVoteCRDTLayer(t *testing.T) {
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	defer h.Close()

	listener := NewListenerNode(context.Background(), h, nil)
	if listener == nil {
		t.Fatal("NewListenerNode returned nil")
	}
	if listener.ListenerBuddyNode == nil {
		t.Fatal("NewListenerNode's StructListener has a nil BuddyNode")
	}
	if listener.ListenerBuddyNode.VoteCRDTLayer == nil {
		t.Fatal("NewListenerNode did not populate VoteCRDTLayer — the real startup path, " +
			"not just node.go's fallback, must set this or Stage 2's dual-write has nothing to write to")
	}
	if listener.ListenerBuddyNode.VoteCRDTLayer.CRDTLayer == nil {
		t.Fatal("VoteCRDTLayer was set but its underlying engine is nil")
	}
	// Both layers must exist and be genuinely distinct engines (see
	// AVC/BuddyNodes/DataLayer/VoteCRDTLayer_test.go for the isolation check
	// itself) — here we only need both non-nil on the real object.
	if listener.ListenerBuddyNode.CRDTLayer == nil {
		t.Fatal("the legacy CRDTLayer must remain populated — Stage 1/2 must not remove it")
	}
}
