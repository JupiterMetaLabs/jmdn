package thebegateway

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// The derived hash must be byte-identical to what orchestrator 01e6341 sends: its
// prover test asserts ProofHash == crypto.Keccak256Hash(envelope).Hex(), and the
// envelope is the block's StarkProof.
func TestCanonicalProofHash(t *testing.T) {
	env := []byte("APRF-envelope-bytes-for-block-888")
	want := crypto.Keccak256Hash(env).Hex()

	t.Run("empty proof_hash derives keccak256(stark), orchestrator format", func(t *testing.T) {
		got := CanonicalProofHash("", env)
		if got != want {
			t.Fatalf("got %s want %s", got, want)
		}
		if len(got) != 66 || !strings.HasPrefix(got, "0x") || got != strings.ToLower(got) {
			t.Fatalf("format: %q must be 0x + 64 lowercase hex (fits CHAR(66))", got)
		}
	})

	t.Run("CHAR(66)-padded blank is treated as empty", func(t *testing.T) {
		if got := CanonicalProofHash(strings.Repeat(" ", 66), env); got != want {
			t.Fatalf("padded blank: got %q want %s", got, want)
		}
	})

	t.Run("a real proof_hash is kept (trimmed)", func(t *testing.T) {
		espresso := "0x" + strings.Repeat("ab", 32)
		if got := CanonicalProofHash(espresso+"  ", env); got != espresso {
			t.Fatalf("got %q want %q", got, espresso)
		}
	})

	t.Run("no proof data yields empty", func(t *testing.T) {
		if got := CanonicalProofHash("", nil); got != "" {
			t.Fatalf("got %q want empty", got)
		}
	})

	t.Run("distinct proofs get distinct hashes (no UNIQUE collision)", func(t *testing.T) {
		a := CanonicalProofHash("", []byte("proof-A"))
		b := CanonicalProofHash("", []byte("proof-B"))
		if a == b {
			t.Fatalf("distinct proofs collided: %s", a)
		}
	})
}
