package Structs

// The legacy vote path's "invalid vote value" branch asserts .(string) on a
// value it has just proved is NOT the expected type:
//
//	voteValue, ok := voteValueRaw.(float64)
//	if !ok {
//	    ... ion.String("vote_value_raw", voteValueRaw.(string)) ...
//	}
//
// An unchecked type assertion inside the branch that handles the wrong type.
// If the value is neither float64 nor string -- JSON null, a bool, an object --
// the assertion panics and takes the node down. The element reaches this point
// from the CRDT, so it is peer-supplied: any node that gossips a malformed
// vote element crashes every legacy-path node that reads it.
//
// Only reachable with JMDN_VOTE_CRDT_V2 unset/false, which is the CODE
// DEFAULT -- so it is exactly the node that missed the env var, already
// running under weaker rules, that also crashes.

import (
	"context"
	"testing"

	"github.com/JupiterMetaLabs/avc/crdt"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/config/PubSubMessages"
)

func legacyNodeWithVoteElement(t *testing.T, elementJSON string) *PubSubMessages.BuddyNode {
	t.Helper()
	ctrl := &Types.Controller{CRDTLayer: crdt.NewEngineMemOnly(8 << 20)}
	if err := DataLayer.Add(ctrl, peer.ID("test-peer"), "votes-key", elementJSON); err != nil {
		t.Fatalf("seeding the CRDT: %v", err)
	}
	return &PubSubMessages.BuddyNode{CRDTLayer: ctrl}
}

func TestLegacyVotePathSurvivesNonStringVoteValue(t *testing.T) {
	// Each of these decodes to something that is neither float64 nor string,
	// so it enters the !ok branch and hits the unchecked assertion.
	for _, tc := range []struct {
		name    string
		element string
	}{
		{"null", `{"vote": null, "block_hash": "0xabc"}`},
		{"bool", `{"vote": true, "block_hash": "0xabc"}`},
		{"object", `{"vote": {"n":1}, "block_hash": "0xabc"}`},
		{"array", `{"vote": [1], "block_hash": "0xabc"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("a malformed vote element panicked the legacy path: %v\n"+
						"This element is peer-supplied via the CRDT, so one node "+
						"gossiping it crashes every legacy-path node that reads it.", rec)
				}
			}()
			node := legacyNodeWithVoteElement(t, tc.element)
			_, _, _ = processVotesFromCRDT_legacy(context.Background(), node, "0xabc")
		})
	}
}
