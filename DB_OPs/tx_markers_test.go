package DB_OPs

import (
	"strconv"
	"testing"
	"time"
)

// TestMarkerValueApplied pins the value-aware marker decision:
// timestamps = applied, -1 = revoked by rollback, legacy garbage = applied.
func TestMarkerValueApplied(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unix timestamp", strconv.FormatInt(time.Now().Unix(), 10), true},
		{"historical dec-2025 value", "1766970000", true},
		{"revoked", "-1", false},
		{"zero", "0", true},
		{"legacy unparseable", "processed", true},
		{"empty", "", true},
	}
	for _, c := range cases {
		if got := markerValueApplied([]byte(c.raw)); got != c.want {
			t.Errorf("%s: markerValueApplied(%q) = %v, want %v", c.name, c.raw, got, c.want)
		}
	}
}

// TestMarkerKeyFormats freezes the on-disk key formats — they must match the
// legacy populations already on disk.
func TestMarkerKeyFormats(t *testing.T) {
	if got := TxProcessedKey("0xabc"); got != "tx_processed:0xabc" {
		t.Errorf("TxProcessedKey: %q", got)
	}
	if got := TxProcessingKey("0xabc"); got != "tx_processing:0xabc" {
		t.Errorf("TxProcessingKey: %q", got)
	}
	if got := BlockProcessedKey("0xdef"); got != "block_processed:0xdef" {
		t.Errorf("BlockProcessedKey: %q", got)
	}
}

// TestMarkerOpEncoding freezes the marker value encoding: decimal string bytes,
// identical to toBytes(int64) / the legacy markers on disk.
func TestMarkerOpEncoding(t *testing.T) {
	op := markerOp("k", 1783583380)
	kv := op.GetKv()
	if string(kv.Key) != "k" || string(kv.Value) != "1783583380" {
		t.Errorf("markerOp: key=%q value=%q", kv.Key, kv.Value)
	}
	if string(markerOp("k", MarkerRevoked).GetKv().Value) != "-1" {
		t.Error("revoked encoding must be \"-1\"")
	}
	// Round-trip through the value-aware decision:
	if markerValueApplied(markerOp("k", MarkerRevoked).GetKv().Value) {
		t.Error("revoked op must decide not-applied")
	}
	if !markerValueApplied(markerOp("k", time.Now().Unix()).GetKv().Value) {
		t.Error("timestamp op must decide applied")
	}
}
