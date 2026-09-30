package backend

import (
	"testing"

	"gossipnode/config"
)

// W1 write side: the anchor body is persisted on anchor blocks and omitted on
// every other block (it is ~29 entries of JSON; ordinary blocks carry none).
func TestToBlockRecord_PersistsCommitteeSnapshotAnchor(t *testing.T) {
	const body = `{"epoch":500}`
	rec := toBlockRecord(&config.ZKBlock{BlockNumber: 860, CommitteeSnapshotAnchor: body, CommitteeSnapshotHash: []byte("h")})
	if got, ok := rec.ExtraData["committee_snapshot_anchor"]; !ok || got != body {
		t.Fatalf("committee_snapshot_anchor = %v (present=%v), want %q", got, ok, body)
	}
	rec = toBlockRecord(&config.ZKBlock{BlockNumber: 861})
	if _, ok := rec.ExtraData["committee_snapshot_anchor"]; ok {
		t.Fatalf("a non-anchor block must not write committee_snapshot_anchor")
	}
}

func TestToBlockRecord_PersistsVdfParamsDigest(t *testing.T) {
	rec := toBlockRecord(&config.ZKBlock{BlockNumber: 900, VdfParamsDigest: "id"})
	if rec.ExtraData["vdf_params_digest"] != "id" {
		t.Fatalf("vdf_params_digest not persisted: %v", rec.ExtraData["vdf_params_digest"])
	}
	if _, ok := toBlockRecord(&config.ZKBlock{BlockNumber: 901}).ExtraData["vdf_params_digest"]; ok {
		t.Fatal("an empty digest must not be written")
	}
}
