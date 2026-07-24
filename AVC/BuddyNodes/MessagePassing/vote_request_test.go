package MessagePassing

import (
	"encoding/json"
	"testing"
)

// Regression for the 2026-07 halt: the buddy must accept block_number whether the
// sequencer sends it as a JSON number (new) or a quoted string (old).
func TestFlexUint64_NumberOrString(t *testing.T) {
	var req struct {
		BlockHash   string     `json:"block_hash"`
		BlockNumber flexUint64 `json:"block_number"`
	}
	// number form (fixed sequencer)
	if err := json.Unmarshal([]byte(`{"block_hash":"0xab","block_number":13222}`), &req); err != nil {
		t.Fatalf("number form must parse: %v", err)
	}
	if uint64(req.BlockNumber) != 13222 {
		t.Fatalf("number form: got %d", req.BlockNumber)
	}
	// string form (old sequencer that caused the halt)
	req.BlockNumber = 0
	if err := json.Unmarshal([]byte(`{"block_hash":"0xab","block_number":"13222"}`), &req); err != nil {
		t.Fatalf("string form must parse (this is the halt case): %v", err)
	}
	if uint64(req.BlockNumber) != 13222 {
		t.Fatalf("string form: got %d", req.BlockNumber)
	}
	// missing / null → zero, no error
	req.BlockNumber = 0
	if err := json.Unmarshal([]byte(`{"block_hash":"0xab"}`), &req); err != nil {
		t.Fatalf("missing block_number must not error: %v", err)
	}
}
