package DB_OPs

// Tests for the epoch-keyed VDF proof store.
//
// These deliberately exercise only what can be checked WITHOUT a live
// ThebeDB: the key scheme, the size contract, and — most importantly — the
// ORDER of the guards in RecordVDFProof. That ordering is the part worth
// pinning: validation must reject a malformed proof before any storage call,
// otherwise a hostile or buggy caller reaches the KV layer with input the
// store has not vetted.
//
// The DB-backed round trip (write, read back, idempotent rewrite, conflicting
// rewrite) needs a running ThebeDB and belongs with the other handle-dependent
// tests under DB_OPs/Tests.

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// reachedStorage reports whether err came from the storage layer rather than
// from RecordVDFProof's own input validation. Without a handle installed,
// getHandle fails — so "the error is not a validation error" is exactly the
// evidence that execution got past the guards.
func reachedStorage(err error) bool {
	return err != nil && !strings.Contains(err.Error(), "refusing to store an empty proof") &&
		!strings.Contains(err.Error(), "exceeds the")
}

func TestVDFProofKeyIsEpochScopedAndDistinct(t *testing.T) {
	if got := VDFProofKey(0); got != "vdf_proof:0" {
		t.Fatalf("key format changed: got %q, want %q — this key is frozen; changing it "+
			"orphans every proof already written on every node", got, "vdf_proof:0")
	}
	if got := VDFProofKey(4096); got != "vdf_proof:4096" {
		t.Fatalf("key format changed: got %q", got)
	}

	seen := make(map[string]uint64, 512)
	for e := uint64(0); e < 512; e++ {
		k := VDFProofKey(e)
		if prev, dup := seen[k]; dup {
			t.Fatalf("epochs %d and %d collide on key %q — one epoch's proof would silently "+
				"overwrite another's", prev, e, k)
		}
		seen[k] = e
	}
}

func TestRecordVDFProofRejectsEmptyBeforeReachingStorage(t *testing.T) {
	err := RecordVDFProof(nil, 7, nil)
	if err == nil {
		t.Fatal("storing an empty proof must fail: an empty record is indistinguishable from " +
			"'no proof', and would be served to a recovering peer as if it were real")
	}
	if reachedStorage(err) {
		t.Fatalf("empty proof reached the storage layer before being rejected: %v", err)
	}

	if err := RecordVDFProof(nil, 7, []byte{}); err == nil {
		t.Fatal("zero-length slice must be rejected exactly like nil")
	}
}

func TestRecordVDFProofRejectsOversizeBeforeReachingStorage(t *testing.T) {
	oversize := make([]byte, MaxVDFProofBytes+1)
	err := RecordVDFProof(nil, 7, oversize)
	if err == nil {
		t.Fatalf("a proof of %d bytes must be refused; the cap is what stops the proof RPC "+
			"being used as a bulk-transfer channel", len(oversize))
	}
	if reachedStorage(err) {
		t.Fatalf("oversize proof reached the storage layer before being rejected: %v", err)
	}
}

func TestRecordVDFProofAcceptsExactlyTheMaximum(t *testing.T) {
	// The off-by-one guard. MaxVDFProofBytes is INCLUSIVE: a proof of exactly
	// that size is legal, so it must get past validation and fail (if at all)
	// on storage instead. A `>=` in place of `>` would break every legitimate
	// proof that happened to land on the boundary.
	atMax := make([]byte, MaxVDFProofBytes)
	for i := range atMax {
		atMax[i] = byte(i)
	}
	err := RecordVDFProof(nil, 7, atMax)
	if err != nil && !reachedStorage(err) {
		t.Fatalf("a proof of exactly MaxVDFProofBytes (%d) was rejected by validation, but the "+
			"bound is inclusive: %v", MaxVDFProofBytes, err)
	}
}

func TestMaxVDFProofBytesFitsAWorstCase2048BitProof(t *testing.T) {
	// Checks the CONSTANT against the real encoder, not against the comment
	// above it. A vdf.Proof marshals as JSON {Y, Pi, T, Group}; Y and Pi are
	// each at most one modulus wide. If MaxVDFProofBytes were ever set below
	// what a legitimate 2048-bit proof encodes to, every honest proof would be
	// refused at the store and the pull path would silently never work.
	maxVal := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 2048), big.NewInt(1))

	worst := vdf.Proof{
		Y:     maxVal,
		Pi:    maxVal,
		T:     ^uint64(0),
		Group: strings.Repeat("g", 64), // generous group-name allowance
	}
	encoded, err := worst.MarshalBinary()
	if err != nil {
		t.Fatalf("marshaling a worst-case proof failed: %v", err)
	}

	if len(encoded) > MaxVDFProofBytes {
		t.Fatalf("MaxVDFProofBytes (%d) is SMALLER than a worst-case 2048-bit proof (%d bytes) — "+
			"legitimate proofs would be refused", MaxVDFProofBytes, len(encoded))
	}
	// Guard the other direction too: the cap should stay a snug multiple of the
	// real size, not an arbitrary large number that stops bounding anything.
	if MaxVDFProofBytes > 8*len(encoded) {
		t.Fatalf("MaxVDFProofBytes (%d) is more than 8x a worst-case proof (%d bytes) — the cap "+
			"has stopped being a meaningful bound", MaxVDFProofBytes, len(encoded))
	}
	t.Logf("worst-case 2048-bit proof encodes to %d bytes; cap is %d", len(encoded), MaxVDFProofBytes)

	// And the encoding must round-trip, since the stored bytes are handed
	// straight to the verifier on the recovery path.
	var back vdf.Proof
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("worst-case proof did not round-trip: %v", err)
	}
	if back.Y.Cmp(maxVal) != 0 || back.Pi.Cmp(maxVal) != 0 || back.T != worst.T || back.Group != worst.Group {
		t.Fatal("worst-case proof round-tripped to a different value")
	}
}
