package DB_OPs

import (
	"strings"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// A zk_proofs row stored with an empty proof_hash before the fix reads back from CHAR(66) as 66 spaces.
// The read path must canonicalize it so every node serves (and fingerprints) the same
// proof_hash for the block.
func TestZKProofRecordToZKBlock_CanonicalizesBlankHash(t *testing.T) {
	stark := []byte("auspex-envelope-first-block")
	blk := &config.ZKBlock{}
	zkProofRecordToZKBlock(&thebegateway.ZKProofRecord{
		BlockNumber: 875,
		ProofHash:   strings.Repeat(" ", 66),
		StarkProof:  stark,
	}, blk)

	if want := crypto.Keccak256Hash(stark).Hex(); blk.ProofHash != want {
		t.Fatalf("ProofHash = %q, want canonical %s", blk.ProofHash, want)
	}
	if string(blk.StarkProof) != string(stark) {
		t.Fatal("StarkProof not restored")
	}
}

func TestZKProofRecordToZKBlock_KeepsRealHash(t *testing.T) {
	real := "0x" + strings.Repeat("cd", 32)
	blk := &config.ZKBlock{}
	zkProofRecordToZKBlock(&thebegateway.ZKProofRecord{ProofHash: real, StarkProof: []byte("x")}, blk)
	if blk.ProofHash != real {
		t.Fatalf("ProofHash = %q, want %q", blk.ProofHash, real)
	}
}
