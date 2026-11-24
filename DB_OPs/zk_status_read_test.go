package DB_OPs

// Regression tests: the ZK status string is persisted in the blocks row's
// ExtraData["zk_status"] (backend.toBlockRecordWithZK) but was never read back, so
// every block read returned Status "" on every node. blockRecordToZKBlock is the
// single BlockRecord->ZKBlock conversion behind GetZKBlockByNumber,
// GetZKBlockByHash and GetBlocksRange.

import (
	"encoding/json"
	"testing"
	"time"

	"gossipnode/DB_OPs/thebegateway"
)

func zkStatusRecord(extra map[string]any) *thebegateway.BlockRecord {
	return &thebegateway.BlockRecord{BlockNumber: 888, Timestamp: time.Unix(1_780_000_000, 0), ExtraData: extra}
}

func TestBlockRecordToZKBlock_RestoresZKStatus(t *testing.T) {
	blk, err := blockRecordToZKBlock(zkStatusRecord(map[string]any{"zk_status": "verified"}))
	if err != nil {
		t.Fatal(err)
	}
	if blk.Status != "verified" {
		t.Fatalf("Status = %q, want %q", blk.Status, "verified")
	}
}

// ExtraData is stored as JSONB; it reaches the reader as a JSON-decoded map.
func TestBlockRecordToZKBlock_RestoresZKStatusAfterJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"zk_status": "verified", "extra_data": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var extra map[string]any
	if err := json.Unmarshal(raw, &extra); err != nil {
		t.Fatal(err)
	}
	blk, err := blockRecordToZKBlock(zkStatusRecord(extra))
	if err != nil {
		t.Fatal(err)
	}
	if blk.Status != "verified" {
		t.Fatalf("Status after JSONB round-trip = %q, want %q", blk.Status, "verified")
	}
}

func TestBlockRecordToZKBlock_MissingOrMalformedZKStatusIsEmpty(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"absent key":       {},
		"nil ExtraData":    nil,
		"non-string value": {"zk_status": 1},
	} {
		blk, err := blockRecordToZKBlock(zkStatusRecord(extra))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if blk.Status != "" {
			t.Fatalf("%s: Status = %q, want empty", name, blk.Status)
		}
	}
}
