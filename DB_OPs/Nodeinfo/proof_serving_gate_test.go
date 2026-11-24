package NodeInfo

// Regression test for the PEER-SERVING DataSync proof gate (Auspex proof-drop).
//
// convertZKBlockToNonHeaders is the converter the FastSync library calls (via
// GetBlockNonHeaders/GetBlockNonHeadersRange) to answer other nodes' DataSync
// requests. It gated the ZK proof on ProofHash != "", so an Auspex block — real
// StarkProof + Commitment but empty proof_hash — was served WITHOUT its proof, and
// every syncing peer stored a proofless block. This test builds an Auspex-style
// block, runs it through the serving converter, and then the receiver's decode step,
// and asserts the proof + commitment survive. It FAILS on 6ef810f (serving gate still
// ProofHash-only) and PASSES once the gate uses config.HasZKProof.

import (
	"bytes"
	"testing"

	"gossipnode/config"
)

func TestConvertZKBlockToNonHeaders_ServesAuspexProof(t *testing.T) {
	stark := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}
	commitment := []uint32{7, 8, 9}

	b := &config.ZKBlock{
		BlockNumber: 888,
		ProofHash:   "", // Auspex: no Espresso-derived proof_hash
		StarkProof:  stark,
		Commitment:  commitment,
	}

	nh := convertZKBlockToNonHeaders(b)

	// Serving side must attach the proof.
	if nh.ZkProof == nil {
		t.Fatal("serving converter dropped the ZK proof for an Auspex block (ProofHash empty but StarkProof+Commitment present)")
	}
	if !bytes.Equal(nh.ZkProof.StarkProof, stark) {
		t.Fatalf("StarkProof not served intact: got %v want %v", nh.ZkProof.StarkProof, stark)
	}

	// Receiver decode step: commitment must round-trip through the wire encoding.
	gotCommitment := bytesToCommitment(nh.ZkProof.Commitment)
	if len(gotCommitment) != len(commitment) {
		t.Fatalf("Commitment length after round-trip: got %d want %d", len(gotCommitment), len(commitment))
	}
	for i := range commitment {
		if gotCommitment[i] != commitment[i] {
			t.Fatalf("Commitment[%d] round-trip: got %d want %d", i, gotCommitment[i], commitment[i])
		}
	}
}

func TestConvertZKBlockToNonHeaders_NoProofDataOmitsZkProof(t *testing.T) {
	// A block with no proof data at all must NOT get an empty ZkProof (no regression:
	// the receiver gates on ZkProof != nil, and an empty proof would be pointless wire).
	b := &config.ZKBlock{BlockNumber: 5}
	if nh := convertZKBlockToNonHeaders(b); nh.ZkProof != nil {
		t.Fatalf("expected no ZkProof for a proofless block, got %+v", nh.ZkProof)
	}
}
