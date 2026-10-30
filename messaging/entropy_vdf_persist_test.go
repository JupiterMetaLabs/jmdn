package messaging

// D-43: the adopt path stored the proof bytes exactly as the block carried
// them. vdf.Proof decodes with json.Unmarshal, which ignores unknown fields, so
// a proposer could pad a valid proof past DB_OPs.MaxVDFProofBytes: every node
// still verified and adopted it, then failed to store it, and answered
// Found:false to every peer recovering that epoch. PersistVDFProof now stores
// the proof's own encoding.

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

func testVDFProofEncoding(t *testing.T) []byte {
	t.Helper()
	raw, err := vdf.Proof{Y: big.NewInt(123456789), Pi: big.NewInt(987654321), T: 1000, Group: "test-group"}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A valid proof with an unknown field big enough to clear MaxVDFProofBytes.
func paddedVDFProofEncoding(t *testing.T) []byte {
	t.Helper()
	canonical := testVDFProofEncoding(t)
	pad := `{"Pad":"` + strings.Repeat("x", DB_OPs.MaxVDFProofBytes) + `",`
	return append([]byte(pad), canonical[1:]...)
}

func captureVDFProofWrites(t *testing.T) *[][]byte {
	t.Helper()
	var stored [][]byte
	orig := recordVDFProofFn
	recordVDFProofFn = func(_ *config.PooledConnection, _ uint64, encoded []byte) error {
		stored = append(stored, append([]byte(nil), encoded...))
		return nil
	}
	t.Cleanup(func() { recordVDFProofFn = orig })
	return &stored
}

func TestPersistVDFProof_StoresCanonicalEncodingOfPaddedProof(t *testing.T) {
	stored := captureVDFProofWrites(t)
	padded := paddedVDFProofEncoding(t)

	var decoded vdf.Proof
	if err := decoded.UnmarshalBinary(padded); err != nil || decoded.Y.Cmp(big.NewInt(123456789)) != 0 {
		t.Fatalf("precondition: the padded encoding must still decode to the same proof (%v)", err)
	}

	if err := PersistVDFProof(7, padded); err != nil {
		t.Fatal(err)
	}
	if len(*stored) != 1 {
		t.Fatalf("got %d writes, want 1", len(*stored))
	}
	got := (*stored)[0]
	if len(got) > DB_OPs.MaxVDFProofBytes {
		t.Fatalf("stored %d bytes, over the %d-byte maximum: the padding was kept, so the proof cannot be stored or served", len(got), DB_OPs.MaxVDFProofBytes)
	}
	if !bytes.Equal(got, testVDFProofEncoding(t)) {
		t.Fatalf("stored %s, want the proof's own encoding %s", got, testVDFProofEncoding(t))
	}
}

// The sealer already stores MarshalBinary's output; it must not change, so the
// sealer's record and an adopter's record for one epoch stay byte-identical
// (RecordVDFProof refuses a second, different encoding).
func TestPersistVDFProof_CanonicalEncodingUnchanged(t *testing.T) {
	stored := captureVDFProofWrites(t)
	canonical := testVDFProofEncoding(t)
	if err := PersistVDFProof(7, canonical); err != nil {
		t.Fatal(err)
	}
	if len(*stored) != 1 || !bytes.Equal((*stored)[0], canonical) {
		t.Fatalf("stored %q, want %q unchanged", *stored, canonical)
	}
}

// Bytes that do not decode were never verified; storing them as given keeps
// the old behaviour (RecordVDFProof still bounds the size).
func TestPersistVDFProof_UndecodableBytesStoredAsGiven(t *testing.T) {
	stored := captureVDFProofWrites(t)
	if err := PersistVDFProof(7, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if len(*stored) != 1 || string((*stored)[0]) != "not json" {
		t.Fatalf("stored %q, want the input unchanged", *stored)
	}
}
