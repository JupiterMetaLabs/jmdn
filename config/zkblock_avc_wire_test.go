package config

// Wire-compatibility tests for the six AVC fields (M1/M2a).
//
// The claim being checked is "old nodes ignore unknown fields". That is only
// true if the actual wire format supports it, so it is tested rather than
// assumed. Block propagation is JSON (messaging/broadcast.go), so these encode
// and decode through encoding/json exactly as the network does.

import (
	"encoding/json"
	"strings"
	"testing"
)

// legacyZKBlock mirrors the pre-AVC struct: the same JSON tags, without the six
// new fields. Standing in for a node running an older build.
type legacyZKBlock struct {
	ProofHash   string `json:"proof_hash"`
	Status      string `json:"status"`
	Timestamp   int64  `json:"timestamp"`
	GasLimit    uint64 `json:"gaslimit"`
	GasUsed     uint64 `json:"gasused"`
	BlockNumber uint64 `json:"blocknumber"`
}

func newBlockWithAVCFields() *ZKBlock {
	return &ZKBlock{
		ProofHash:           "0xproof",
		Status:              "committed",
		Timestamp:           1700000000,
		GasLimit:            30000000,
		GasUsed:             21000,
		BlockNumber:         500,
		Slot:                512,
		Period:              2,
		RandaoReveals:       []Reveal{{ProposerID: "peerA", Secret: []byte("s")}},
		VdfProof:            []byte("proof"),
		SeedEpoch:           30,
		VotingSnapshotEpoch: 31,
	}
}

// TestOldNodeDecodesNewBlock is the forward-compatibility direction: a new
// block must still decode cleanly on an old node, with every pre-existing
// field intact and the unknown keys simply dropped.
func TestOldNodeDecodesNewBlock(t *testing.T) {
	raw, err := json.Marshal(newBlockWithAVCFields())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old legacyZKBlock
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("old node failed to decode a new block: %v", err)
	}

	if old.BlockNumber != 500 || old.GasUsed != 21000 || old.Status != "committed" {
		t.Fatalf("old fields corrupted: %+v", old)
	}
}

// TestNewNodeDecodesOldBlock is the backward direction: a block from an old
// node has no AVC keys, and they must read as zero rather than failing.
func TestNewNodeDecodesOldBlock(t *testing.T) {
	raw, err := json.Marshal(legacyZKBlock{
		ProofHash: "0xproof", Status: "committed", BlockNumber: 499,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var b ZKBlock
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("new node failed to decode an old block: %v", err)
	}

	if b.BlockNumber != 499 {
		t.Fatalf("BlockNumber = %d, want 499", b.BlockNumber)
	}
	if b.Slot != 0 || b.Period != 0 || b.SeedEpoch != 0 || b.VotingSnapshotEpoch != 0 {
		t.Fatal("absent AVC fields should decode as zero")
	}
	if b.RandaoReveals != nil || b.VdfProof != nil {
		t.Fatal("absent slice fields should decode as nil")
	}
}

// TestAVCFieldsRoundTrip checks the values survive encode/decode unchanged.
func TestAVCFieldsRoundTrip(t *testing.T) {
	orig := newBlockWithAVCFields()

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ZKBlock
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	switch {
	case got.Slot != orig.Slot:
		t.Fatalf("Slot = %d, want %d", got.Slot, orig.Slot)
	case got.Period != orig.Period:
		t.Fatalf("Period = %d, want %d", got.Period, orig.Period)
	case got.SeedEpoch != orig.SeedEpoch:
		t.Fatalf("SeedEpoch = %d, want %d", got.SeedEpoch, orig.SeedEpoch)
	case got.VotingSnapshotEpoch != orig.VotingSnapshotEpoch:
		t.Fatalf("VotingSnapshotEpoch = %d, want %d", got.VotingSnapshotEpoch, orig.VotingSnapshotEpoch)
	case string(got.VdfProof) != string(orig.VdfProof):
		t.Fatalf("VdfProof = %q, want %q", got.VdfProof, orig.VdfProof)
	case len(got.RandaoReveals) != 1:
		t.Fatalf("RandaoReveals length = %d, want 1", len(got.RandaoReveals))
	case got.RandaoReveals[0].ProposerID != "peerA":
		t.Fatalf("ProposerID = %q, want peerA", got.RandaoReveals[0].ProposerID)
	}
}

// TestEmptyAVCFieldsAreOmitted confirms omitempty works: a block with no AVC
// values must serialize byte-identically to how it did before the fields
// existed, so nothing changes for blocks that do not use them yet.
func TestEmptyAVCFieldsAreOmitted(t *testing.T) {
	raw, err := json.Marshal(&ZKBlock{BlockNumber: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{
		"slot", "period", "randao_reveals", "vdf_proof",
		"seed_epoch", "voting_snapshot_epoch",
	} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("empty block should not emit %q, got: %s", key, raw)
		}
	}
}
