package backend

import (
	"testing"

	"gossipnode/config"
)

// Pins the write half of the zk_status contract: DB_OPs.blockRecordToZKBlock reads
// ExtraData["zk_status"] back into ZKBlock.Status, so renaming the key here (or there)
// must break a test rather than silently blanking Status again.
func TestToBlockRecordWithZK_PersistsZKStatus(t *testing.T) {
	rec := toBlockRecordWithZK(&config.ZKBlock{BlockNumber: 888, Status: "verified"})
	if got := rec.ExtraData["zk_status"]; got != "verified" {
		t.Fatalf(`ExtraData["zk_status"] = %v, want "verified"`, got)
	}
}
