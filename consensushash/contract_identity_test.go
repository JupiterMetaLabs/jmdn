package consensushash

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func caddr(b byte) common.Address { var a common.Address; a[19] = b; return a }

// The derivation must be deterministic (sequencer and every validator compute the
// same ordinal for a given contract address) and never the zero sentinel.
func TestDeriveContractARTNonce_DeterministicNonZero(t *testing.T) {
	a := caddr(7)
	got1 := DeriveContractARTNonce(a)
	got2 := DeriveContractARTNonce(a)
	if got1 != got2 {
		t.Fatalf("non-deterministic: %d != %d", got1, got2)
	}
	if got1 == 0 {
		t.Fatal("must never return the zero sentinel")
	}
}

// Distinct addresses should (overwhelmingly) yield distinct ordinals.
func TestDeriveContractARTNonce_DistinctAddresses(t *testing.T) {
	seen := map[uint64]byte{}
	for b := byte(1); b <= 200; b++ {
		n := DeriveContractARTNonce(caddr(b))
		if prev, ok := seen[n]; ok {
			t.Fatalf("collision: addr %d and %d both -> %d", prev, b, n)
		}
		seen[n] = b
	}
}

// Domain-tagged: the all-zero address must not produce the zero ordinal (guards
// against an untagged keccak of empty/zero input degenerating).
func TestDeriveContractARTNonce_ZeroAddressNonZero(t *testing.T) {
	if DeriveContractARTNonce(common.Address{}) == 0 {
		t.Fatal("zero address produced the zero ordinal")
	}
}
