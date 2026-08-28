package Security

// M2b activation flag tests. The user's own warning: M0 makes `period`
// chain-derived, but not tamper-proof - that requires M2b (fold the six
// consensus fields into the block hash) to actually be LIVE, not just built.
// These tests pin the flag's two required properties: off is a true no-op
// (byte-identical to pre-M2b behavior), and on actually closes the tamper
// gap M0 alone leaves open.

import (
	"testing"

	"gossipnode/config"
)

// TestM2bFlagDefaultsOff pins the rollout safety property directly: without
// anyone setting JMDN_M2B_HASH, M2bHashEnabled must be false, so a fresh
// process behaves exactly as it did before M2b existed.
func TestM2bFlagDefaultsOff(t *testing.T) {
	if M2bHashEnabled {
		t.Fatal("M2bHashEnabled must default to false - flipping it is a coordinated rollout, never a default")
	}
}

// TestCheckBlockHash_FlagOff_LegacyBehaviorUnchanged proves the flag being
// off is a true no-op: mutating a consensus field must NOT affect
// CheckBlockHash's verdict, exactly as before M2b was wired in. This is the
// regression guard for "landing M2b broke behavior for everyone who hasn't
// opted in yet."
func TestCheckBlockHash_FlagOff_LegacyBehaviorUnchanged(t *testing.T) {
	txs := []config.Transaction{legacyTx(0, 1)}
	block := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromContents(txs),
		Transactions: txs,
		Period:       1,
	}
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag off: legacy-hash block should pass, got ok=%v err=%v", ok, err)
	}

	block.Period = 2 // mutate the consensus field the legacy hash never covered
	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag off: mutating Period must NOT affect the legacy check, got ok=%v err=%v", ok, err)
	}
}

// TestCheckBlockHash_FlagOn_ClosesTheTamperGap is the actual point of this
// work: with the flag on, a block whose Period was silently rewritten after
// its BlockHash was computed must now be rejected - the exact attack M0
// alone (period is derived correctly, but nothing stops a relay from
// overwriting the field in transit) leaves open.
func TestCheckBlockHash_FlagOn_ClosesTheTamperGap(t *testing.T) {
	M2bHashEnabled = true
	defer func() { M2bHashEnabled = false }()

	txs := []config.Transaction{legacyTx(0, 1)}
	block := &config.ZKBlock{Transactions: txs, Slot: 10, Period: 1, SeedEpoch: 5, VotingSnapshotEpoch: 5}
	block.BlockHash = RecomputeBlockHashWithConsensusFields(block)

	if ok, err := CheckBlockHash(block); err != nil || !ok {
		t.Fatalf("flag on: correctly-hashed M2b block should pass, got ok=%v err=%v", ok, err)
	}

	// A relay silently rewrites Period after the hash was computed - the
	// exact scenario the user's warning describes.
	block.Period = 99
	if ok, _ := CheckBlockHash(block); ok {
		t.Fatal("flag on: a block with a rewritten Period must be rejected - this is the whole point of M2b")
	}
}

// TestCheckBlockHash_FlagOn_RejectsPreM2bGeneratorOutput is the fail-safe
// property for an uncoordinated flip: if the validator flips to M2b but the
// generator has NOT (still producing legacy-formula hashes), blocks must be
// rejected outright, never silently accepted under the wrong formula. This
// is what makes it safe for an operator to flip the flag defensively even
// before confirming the generator side - it fails closed, not open.
func TestCheckBlockHash_FlagOn_RejectsPreM2bGeneratorOutput(t *testing.T) {
	M2bHashEnabled = true
	defer func() { M2bHashEnabled = false }()

	txs := []config.Transaction{legacyTx(0, 1)}
	block := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromContents(txs), // legacy-formula hash, as a not-yet-upgraded generator would produce
		Transactions: txs,
		Period:       1,
	}
	if ok, _ := CheckBlockHash(block); ok {
		t.Fatal("flag on: a legacy-formula block hash must be rejected, not accepted under the new formula")
	}
}
