package backend

import (
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// Write path: an Auspex block (StarkProof, no proof_hash) must get the canonical
// keccak256(StarkProof) hash on BOTH the zk_proofs record and the block's ExtraData,
// and two such blocks must get distinct hashes (no zk_proofs.proof_hash UNIQUE collision).
func TestToZKProofRecord_DerivesProofHashForAuspex(t *testing.T) {
	a := &config.ZKBlock{BlockNumber: 900, StarkProof: []byte("env-900"), Commitment: []uint32{1, 2}}
	b := &config.ZKBlock{BlockNumber: 901, StarkProof: []byte("env-901")}

	ra, rb := toZKProofRecord(a), toZKProofRecord(b)
	if want := crypto.Keccak256Hash(a.StarkProof).Hex(); ra.ProofHash != want {
		t.Fatalf("block 900 proof_hash = %q, want %s", ra.ProofHash, want)
	}
	if ra.ProofHash == rb.ProofHash {
		t.Fatalf("two Auspex blocks got the same proof_hash %s (would collide on UNIQUE)", ra.ProofHash)
	}
	if got := toBlockRecordWithZK(a).ExtraData["proof_hash"]; got != ra.ProofHash {
		t.Fatalf("block ExtraData proof_hash %v != zk_proofs proof_hash %s", got, ra.ProofHash)
	}
}

func TestToZKProofRecord_KeepsOrchestratorHash(t *testing.T) {
	blk := &config.ZKBlock{BlockNumber: 902, StarkProof: []byte("env-902")}
	blk.ProofHash = crypto.Keccak256Hash(blk.StarkProof).Hex() // what 01e6341 sends
	if got := toZKProofRecord(blk).ProofHash; got != blk.ProofHash {
		t.Fatalf("got %q want %q", got, blk.ProofHash)
	}
}
