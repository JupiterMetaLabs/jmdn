package MessagePassing

// A8-1 tests (docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md): the v2 CRDT
// merge path (mergeVoteCRDTElement) must apply the same two guards AddVote
// enforces on its own write path — the compaction watermark and the
// per-peer ingest cap — which it previously bypassed by writing through
// avcdatalayer.Add directly.

import (
	"encoding/json"
	"testing"

	"gossipnode/AVC/BuddyNodes/CRDTSync"

	avcdatalayer "github.com/JupiterMetaLabs/avc/buddynodes/datalayer"
	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// withFreshWatermark swaps avcvotes.DefaultWatermark for an isolated one for
// the duration of the test, restoring the original afterward — the same
// save/swap/restore-on-cleanup pattern timeout_certificates_test.go uses for
// DefaultPeriodStore. Required because DefaultWatermark is process-wide
// state shared across every test in this binary, and Watermark.Set refuses
// to move backward, so a test that raises it could never put it back.
func withFreshWatermark(t *testing.T) {
	t.Helper()
	saved := avcvotes.DefaultWatermark
	avcvotes.DefaultWatermark = avcvotes.NewWatermark()
	t.Cleanup(func() { avcvotes.DefaultWatermark = saved })
}

func TestMergeVoteCRDTElement_RejectsHeightAtOrBelowWatermark(t *testing.T) {
	withFreshWatermark(t)
	if err := avcvotes.DefaultWatermark.Set(200, 100); err != nil { // watermark -> 100
		t.Fatalf("Set: %v", err)
	}

	node, _ := freshTestNode(t)
	_, senderID := freshTestNode(t)

	const height, blockHash = uint64(50), "0xstale" // 50 <= watermark(100)
	voteKey := avcvotes.BlockVoteKey(height, blockHash)

	syncMsg := CRDTSync.Message{
		NodeID: senderID.String(),
		SyncData: map[string]json.RawMessage{
			voteKey: rawLWWSetJSON(t, voteKey, senderID.String()+":1"),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData: %v", err)
	}

	if _, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, voteKey); ok {
		t.Fatal("a vote for an already-converged height must not be resurrected by a merge")
	}
}

func TestMergeVoteCRDTElement_AllowsHeightAboveWatermark(t *testing.T) {
	withFreshWatermark(t)
	if err := avcvotes.DefaultWatermark.Set(200, 100); err != nil { // watermark -> 100
		t.Fatalf("Set: %v", err)
	}

	node, _ := freshTestNode(t)
	_, senderID := freshTestNode(t)

	const height, blockHash = uint64(150), "0xlive" // 150 > watermark(100)
	voteKey := avcvotes.BlockVoteKey(height, blockHash)

	syncMsg := CRDTSync.Message{
		NodeID: senderID.String(),
		SyncData: map[string]json.RawMessage{
			voteKey: rawLWWSetJSON(t, voteKey, senderID.String()+":1"),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData: %v", err)
	}

	set, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, voteKey)
	if !ok || len(set) != 1 {
		t.Fatalf("a vote above the watermark must still merge normally, got %v (ok=%v)", set, ok)
	}
}

func TestMergeVoteCRDTElement_EnforcesPerPeerIngestCap(t *testing.T) {
	withFreshWatermark(t) // watermark stays 0 (fresh) — nothing filtered by height here

	node, _ := freshTestNode(t)
	_, senderID := freshTestNode(t)
	_, attackerID := freshTestNode(t)

	const height, blockHash = uint64(10), "0xflood"
	voteKey := avcvotes.BlockVoteKey(height, blockHash)

	// One peer offering 6 distinct elements in a single merge — well beyond
	// avcvotes.MaxElementsPerPeerPerBlock (3) — must be capped, not admitted
	// wholesale.
	elems := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		elems = append(elems, attackerID.String()+":"+string(rune('0'+i)))
	}

	syncMsg := CRDTSync.Message{
		NodeID: senderID.String(),
		SyncData: map[string]json.RawMessage{
			voteKey: rawLWWSetJSON(t, voteKey, elems...),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData: %v", err)
	}

	set, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, voteKey)
	if !ok {
		t.Fatal("expected the key to exist with the admitted elements")
	}
	if len(set) != avcvotes.MaxElementsPerPeerPerBlock {
		t.Fatalf("got %d elements admitted for one peer, want exactly the cap (%d): %v",
			len(set), avcvotes.MaxElementsPerPeerPerBlock, set)
	}
}
