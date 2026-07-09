package NodeInfo

// Tests for the payloadTypeTxMarkers drain path.

import "testing"

func TestParseTxMarkersPayload_Valid(t *testing.T) {
	payload := `[{"hash":"0xaaa","applied_at":1783583380},{"hash":"0xbbb","applied_at":1783583381}]`
	wires, err := parseTxMarkersPayload(payload)
	if err != nil {
		t.Fatalf("parseTxMarkersPayload: %v", err)
	}
	if len(wires) != 2 || wires[0].Hash != "0xaaa" || wires[0].AppliedAt != 1783583380 {
		t.Fatalf("unexpected wires: %+v", wires)
	}
}

func TestParseTxMarkersPayload_Poison(t *testing.T) {
	// Broken JSON.
	if _, err := parseTxMarkersPayload(`{nope`); err == nil {
		t.Fatal("want error for broken JSON")
	}
	// Empty hash.
	if _, err := parseTxMarkersPayload(`[{"hash":"","applied_at":1}]`); err == nil {
		t.Fatal("want error for empty hash")
	}
	// Non-positive applied_at: a -1 arriving on this wire would REVOKE a
	// legitimate live-path marker at drain time — must be rejected as poison.
	if _, err := parseTxMarkersPayload(`[{"hash":"0xaaa","applied_at":-1}]`); err == nil {
		t.Fatal("want error for applied_at=-1 (revocation must never ride the marker wire)")
	}
	if _, err := parseTxMarkersPayload(`[{"hash":"0xaaa","applied_at":0}]`); err == nil {
		t.Fatal("want error for applied_at=0")
	}
}
