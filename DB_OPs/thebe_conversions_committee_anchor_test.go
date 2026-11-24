package DB_OPs

import (
	"testing"

	"gossipnode/DB_OPs/thebegateway"
)

// W1: an anchor block's committee snapshot body must survive the JSONB round
// trip byte-for-byte - a node that restarts rebuilds the anchored pool from its
// own stored anchor block, and re-verifies the body against the hash-bound
// CommitteeSnapshotHash. Any change to the string breaks that proof.
func TestBlockRecordToZKBlock_RecoversCommitteeSnapshotAnchor(t *testing.T) {
	body := `{"epoch":500,"entries":[{"peer_id":"12D3KooWAlice","bls_pub":"aa","reward_address":"0x01"}],"authority_pub_hex":"bb","signature":"cc"}`
	rec := &thebegateway.BlockRecord{
		BlockNumber: 860,
		ExtraData: throughJSON(t, map[string]any{
			"committee_snapshot_anchor": body,
			"committee_snapshot_hash":   []byte("anchor-hash"),
		}),
	}
	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: %v", err)
	}
	if blk.CommitteeSnapshotAnchor != body {
		t.Fatalf("anchor body changed in the round trip:\n got %q\nwant %q", blk.CommitteeSnapshotAnchor, body)
	}
}

func TestBlockRecordToZKBlock_CommitteeSnapshotAnchorAbsentIsEmpty(t *testing.T) {
	blk, err := blockRecordToZKBlock(&thebegateway.BlockRecord{BlockNumber: 861, ExtraData: map[string]any{}})
	if err != nil || blk.CommitteeSnapshotAnchor != "" {
		t.Fatalf("a non-anchor block must decode with an empty anchor (err=%v)", err)
	}
}

func TestBlockRecordToZKBlock_MalformedCommitteeSnapshotAnchorFailsClosed(t *testing.T) {
	_, err := blockRecordToZKBlock(&thebegateway.BlockRecord{
		BlockNumber: 860,
		ExtraData:   map[string]any{"committee_snapshot_anchor": map[string]any{"epoch": 1.0}},
	})
	if err == nil {
		t.Fatal("a non-string anchor must be an error, not a silently missing anchor")
	}
}

// VdfParamsDigest is a ConsensusHash input; before this it was dropped on
// write, so a stored epoch-boundary block no longer recomputed to its own
// ConsensusHash. The committee-anchor binding check recomputes stored blocks.
func TestBlockRecordToZKBlock_RecoversVdfParamsDigest(t *testing.T) {
	rec := &thebegateway.BlockRecord{
		BlockNumber: 900,
		ExtraData:   throughJSON(t, map[string]any{"vdf_params_digest": "wesolowski|rsa2048|T=1000000"}),
	}
	blk, err := blockRecordToZKBlock(rec)
	if err != nil || blk.VdfParamsDigest != "wesolowski|rsa2048|T=1000000" {
		t.Fatalf("VdfParamsDigest = %q (err=%v)", blk.VdfParamsDigest, err)
	}
	if _, err := blockRecordToZKBlock(&thebegateway.BlockRecord{BlockNumber: 900, ExtraData: map[string]any{"vdf_params_digest": 7.0}}); err == nil {
		t.Fatal("a non-string vdf_params_digest must fail closed")
	}
}
