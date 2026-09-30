package FastsyncV2

// Regression test for the DataSync proof-propagation gate (Auspex proof-drop).
//
// Before the fix, zkBlockToProtoNonHeaders attached the ZK proof only when
// ProofHash != "". Auspex-produced blocks carry StarkProof + Commitment but no
// ProofHash (proof_hash was Espresso-derived; the orchestrator stopped setting
// it), so DataSync dropped the proof entirely: synced nodes stored proofless
// blocks, re-fetched them forever (blockNeedsDataSync), and diverged in the
// SyncMonitor Merkle fingerprint (StarkProof is folded into internal/merkle.hashBlock).

import (
	"testing"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

func TestZkBlockToProtoNonHeaders_ProofGate(t *testing.T) {
	cases := []struct {
		name       string
		proofHash  string
		starkProof []byte
		commitment []uint32
		wantProof  bool
	}{
		{"auspex: stark+commitment, no proof_hash", "", []byte{1, 2, 3, 4}, []uint32{7, 8}, true},
		{"stark only, no proof_hash", "", []byte{9, 9}, nil, true},
		{"commitment only, no proof_hash", "", nil, []uint32{1}, true},
		{"legacy: proof_hash set", "0xabc", []byte{1}, []uint32{1}, true},
		{"no proof data at all", "", nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &types.ZKBlock{
				BlockNumber: 888,
				ProofHash:   c.proofHash,
				StarkProof:  c.starkProof,
				Commitment:  c.commitment,
			}
			nh := zkBlockToProtoNonHeaders(b)
			if (nh.ZkProof != nil) != c.wantProof {
				t.Fatalf("ZkProof present=%v, want %v", nh.ZkProof != nil, c.wantProof)
			}
			if !c.wantProof {
				return
			}
			// When present, the proof fields must survive the conversion.
			if string(nh.ZkProof.StarkProof) != string(c.starkProof) {
				t.Fatalf("StarkProof not carried: got %v want %v", nh.ZkProof.StarkProof, c.starkProof)
			}
			if nh.ZkProof.ProofHash != c.proofHash {
				t.Fatalf("ProofHash not carried: got %q want %q", nh.ZkProof.ProofHash, c.proofHash)
			}
			// commitmentToBytes emits 4 bytes per uint32.
			if want := len(c.commitment) * 4; len(nh.ZkProof.Commitment) != want {
				t.Fatalf("Commitment length: got %d want %d", len(nh.ZkProof.Commitment), want)
			}
		})
	}
}
