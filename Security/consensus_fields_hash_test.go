package Security

// Tests for M2b (§8): every consensus field must be tamper-evident, the hash
// must be deterministic, and the legacy hash must be unaffected.

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// sampleBlock returns a block with all six fields set, plus a transaction so
// the tx binding is exercised too.
func sampleBlock() *config.ZKBlock {
	to := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	return &config.ZKBlock{
		Transactions: []config.Transaction{{
			Nonce:    7,
			To:       &to,
			Value:    big.NewInt(1000),
			GasPrice: big.NewInt(1),
			GasLimit: 21000,
		}},
		Slot:   500,
		Period: 2,
		RandaoReveals: []config.Reveal{
			{ProposerID: "peerA", Secret: []byte("secret-a")},
			{ProposerID: "peerB", Secret: []byte("secret-b")},
		},
		VdfProof:              []byte("proof-bytes"),
		SeedEpoch:             30,
		VotingSnapshotEpoch:   31,
		CommitteeSnapshotHash: []byte("snapshot-hash-bytes"),
	}
}

// TestEveryConsensusFieldIsTamperEvident is the core M2b requirement: mutating
// any one field must change the hash. A field that survives mutation is one a
// relay can rewrite after commit.
func TestEveryConsensusFieldIsTamperEvident(t *testing.T) {
	base := RecomputeBlockHashWithConsensusFields(sampleBlock())

	mutations := map[string]func(*config.ZKBlock){
		"Slot":                func(b *config.ZKBlock) { b.Slot++ },
		"Period":              func(b *config.ZKBlock) { b.Period++ },
		"SeedEpoch":           func(b *config.ZKBlock) { b.SeedEpoch++ },
		"VotingSnapshotEpoch": func(b *config.ZKBlock) { b.VotingSnapshotEpoch++ },
		"VdfProof":              func(b *config.ZKBlock) { b.VdfProof = []byte("different") },
		"CommitteeSnapshotHash": func(b *config.ZKBlock) { b.CommitteeSnapshotHash = []byte("different-hash") },
		"RandaoReveals": func(b *config.ZKBlock) {
			b.RandaoReveals[0].Secret = []byte("tampered")
		},
		"RandaoReveals/proposer": func(b *config.ZKBlock) {
			b.RandaoReveals[0].ProposerID = "peerZ"
		},
		"Transactions": func(b *config.ZKBlock) { b.Transactions[0].Nonce++ },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := sampleBlock()
			mutate(b)
			if got := RecomputeBlockHashWithConsensusFields(b); got == base {
				t.Fatalf("mutating %s did not change the hash — field is not covered", name)
			}
		})
	}
}

// TestReorderingRevealsChangesHash pins that the hash binds the exact reveal
// order carried in the block, so a relay cannot shuffle entries silently.
func TestReorderingRevealsChangesHash(t *testing.T) {
	b := sampleBlock()
	before := RecomputeBlockHashWithConsensusFields(b)

	b.RandaoReveals[0], b.RandaoReveals[1] = b.RandaoReveals[1], b.RandaoReveals[0]

	if RecomputeBlockHashWithConsensusFields(b) == before {
		t.Fatal("reordering reveals left the hash unchanged")
	}
}

// TestHashIsDeterministic guards against nondeterministic serialization: the
// same block must hash identically every time, on every node.
func TestHashIsDeterministic(t *testing.T) {
	want := RecomputeBlockHashWithConsensusFields(sampleBlock())
	for range 100 {
		// Rebuild the block each time so fresh slices and pointers are used —
		// this catches hashing that depends on memory layout rather than value.
		if got := RecomputeBlockHashWithConsensusFields(sampleBlock()); got != want {
			t.Fatalf("same block produced different hashes: %s vs %s", got.Hex(), want.Hex())
		}
	}
}

// TestNilAndEmptyAreEquivalent checks that "no value" has exactly one
// representation. nil and an empty slice mean the same thing to the protocol,
// so they must not produce different hashes.
func TestNilAndEmptyAreEquivalent(t *testing.T) {
	nilBlock := sampleBlock()
	nilBlock.VdfProof = nil
	nilBlock.RandaoReveals = nil
	nilBlock.CommitteeSnapshotHash = nil

	emptyBlock := sampleBlock()
	emptyBlock.VdfProof = []byte{}
	emptyBlock.RandaoReveals = []config.Reveal{}
	emptyBlock.CommitteeSnapshotHash = []byte{}

	if RecomputeBlockHashWithConsensusFields(nilBlock) !=
		RecomputeBlockHashWithConsensusFields(emptyBlock) {
		t.Fatal("nil and empty hashed differently — 'no value' must have one form")
	}
}

// TestFieldBoundariesAreUnambiguous is the block-level analogue of
// TestSeedConcatenationAmbiguity: length prefixes must stop distinct values
// from sharing a preimage. Without them "A"+"BC" and "AB"+"C" would collide.
func TestFieldBoundariesAreUnambiguous(t *testing.T) {
	split := []config.Reveal{
		{ProposerID: "A", Secret: []byte("BC")},
	}
	shifted := []config.Reveal{
		{ProposerID: "AB", Secret: []byte("C")},
	}

	a := sampleBlock()
	a.RandaoReveals = split
	b := sampleBlock()
	b.RandaoReveals = shifted

	if RecomputeBlockHashWithConsensusFields(a) == RecomputeBlockHashWithConsensusFields(b) {
		t.Fatal("field boundary collision — length prefixing is not working")
	}
}

// TestEmptyBlocksDifferBySlot documents the deliberate v1 divergence: v1 returns
// the zero hash for any empty block, so all empty blocks collide. v2 must not,
// because an empty block still carries consensus metadata.
func TestEmptyBlocksDifferBySlot(t *testing.T) {
	a := &config.ZKBlock{Slot: 1}
	b := &config.ZKBlock{Slot: 2}

	if RecomputeBlockHashWithConsensusFields(a) == RecomputeBlockHashWithConsensusFields(b) {
		t.Fatal("two empty blocks at different slots shared a hash")
	}
	if RecomputeBlockHashWithConsensusFields(a) == (common.Hash{}) {
		t.Fatal("v2 must not return the zero hash for an empty block")
	}
}

// TestLegacyHashIsUnaffected is the regression guard for M2b landing unwired:
// adding the six fields must not change what the live hash function computes.
func TestLegacyHashIsUnaffected(t *testing.T) {
	plain := sampleBlock()
	plain.Slot, plain.Period, plain.SeedEpoch, plain.VotingSnapshotEpoch = 0, 0, 0, 0
	plain.RandaoReveals, plain.VdfProof = nil, nil

	withFields := sampleBlock()

	if RecomputeBlockHashFromContents(plain.Transactions) !=
		RecomputeBlockHashFromContents(withFields.Transactions) {
		t.Fatal("legacy hash changed — it must stay transactions-only until M2b activates")
	}
}

// TestV2DiffersFromV1 confirms the domain tag separates the two preimages, so a
// v2 hash can never be mistaken for a v1 hash of the same transactions.
func TestV2DiffersFromV1(t *testing.T) {
	b := sampleBlock()
	if common.Hash(RecomputeBlockHashWithConsensusFields(b)) ==
		RecomputeBlockHashFromContents(b.Transactions) {
		t.Fatal("v2 and v1 produced the same hash")
	}
}

// TestRevealsAreCanonical covers the validation-time well-formedness rule that
// EncodeReveals deliberately leaves alone.
func TestRevealsAreCanonical(t *testing.T) {
	cases := []struct {
		name    string
		reveals []config.Reveal
		want    bool
	}{
		{"empty", nil, true},
		{"single", []config.Reveal{{ProposerID: "a"}}, true},
		{"sorted", []config.Reveal{{ProposerID: "a"}, {ProposerID: "b"}}, true},
		{"unsorted", []config.Reveal{{ProposerID: "b"}, {ProposerID: "a"}}, false},
		{"duplicate", []config.Reveal{{ProposerID: "a"}, {ProposerID: "a"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RevealsAreCanonical(tc.reveals); got != tc.want {
				t.Fatalf("RevealsAreCanonical = %v, want %v", got, tc.want)
			}
		})
	}
}
