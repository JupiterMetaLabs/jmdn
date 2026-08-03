package adapters_test

import (
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/JupiterMetaLabs/avc/validation"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
	"gossipnode/consensus/adapters"
	"gossipnode/messaging"
)

// This is the A3 hot-spot proof: that avc's ported recompute functions produce
// the byte-identical block hash and txns root that jmdn's OWN generators
// produce, when both are fed the same real config.ZKBlock through the adapter.
//
// If this ever fails, the entire structural-validation foundation is invalid —
// avc would reject every honest block — and no further wiring should proceed
// until it is green again. jmdn's generators
// (messaging.RecomputeBlockHashFromTxs / messaging.RecomputeTxnsRoot) are pure
// functions over the transaction list (no DB), so this runs standalone.
//
// NOTE: this file lives in package adapters_test (external test package), not
// package adapters, SPECIFICALLY to avoid an import cycle: this file imports
// gossipnode/messaging, and messaging imports gossipnode/Vote, which (as of
// the A3 wiring) imports gossipnode/consensus/adapters. Putting this test in
// the same internal package as adapters would make the test binary's import
// graph cyclic. Using the external adapters_test package breaks the cycle
// because adapters (the production package) itself never imports messaging.

// tx builds a config.Transaction with a deterministic 32-byte hash from a seed
// byte, mirroring how the block generator populates Transaction.Hash upstream.
func tx(seed byte) config.Transaction {
	var h common.Hash
	for i := range h {
		h[i] = seed
	}
	return config.Transaction{Hash: h}
}

// realBlock builds a block whose BlockHash and TxnsRoot are computed by jmdn's
// OWN generators — i.e. a self-consistent block exactly as jmdn would produce
// it. The adapter + avc must then agree with those values.
func realBlock(txs []config.Transaction) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber:  7,
		Transactions: txs,
		BlockHash:    messaging.RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     messaging.RecomputeTxnsRoot(txs),
	}
}

func TestParity_BlockHash_AvcMatchesJmdn(t *testing.T) {
	cases := [][]config.Transaction{
		{tx(0x01)},                          // single tx (self-paired path)
		{tx(0x01), tx(0x02)},                // even
		{tx(0xAA), tx(0xBB), tx(0xCC)},      // odd (last-node duplicate path)
		{tx(1), tx(2), tx(3), tx(4), tx(5)}, // larger odd
	}
	for i, txs := range cases {
		blk := realBlock(txs)
		ad := adapters.NewZKBlockAdapter(blk)

		jmdnHash := blk.BlockHash.Hex() // jmdn's generator output
		avcHash := validation.RecomputeBlockHashFromTxs(ad.Transactions())
		if !strings.EqualFold(jmdnHash, avcHash) {
			t.Fatalf("case %d (%d txs): BLOCK HASH mismatch\n jmdn %s\n avc  %s\n"+
				"→ avc's Keccak256 recompute does NOT match jmdn's — structural validation would reject every block",
				i, len(txs), jmdnHash, avcHash)
		}
	}
}

func TestParity_TxnsRoot_AvcMatchesJmdn(t *testing.T) {
	cases := [][]config.Transaction{
		{tx(0x01)},
		{tx(0x01), tx(0x02)},
		{tx(0xAA), tx(0xBB), tx(0xCC)},
		{tx(1), tx(2), tx(3), tx(4), tx(5)},
	}
	for i, txs := range cases {
		blk := realBlock(txs)
		ad := adapters.NewZKBlockAdapter(blk)

		jmdnRoot := blk.TxnsRoot // jmdn's generator output
		avcRoot := validation.RecomputeTxnsRoot(ad.Transactions())
		if !strings.EqualFold(jmdnRoot, avcRoot) {
			t.Fatalf("case %d (%d txs): TXNS ROOT mismatch\n jmdn %s\n avc  %s\n"+
				"→ avc's SHA-256 merkle recompute does NOT match jmdn's",
				i, len(txs), jmdnRoot, avcRoot)
		}
	}
}

// TestParity_StructuralValidatorAcceptsRealBlock runs the WHOLE avc structural
// validator against a real jmdn block through the adapter — the end-to-end
// path a buddy node would take. A self-consistent jmdn block must be approved.
func TestParity_StructuralValidatorAcceptsRealBlock(t *testing.T) {
	blk := realBlock([]config.Transaction{tx(0xAA), tx(0xBB), tx(0xCC)})
	ad := adapters.NewZKBlockAdapter(blk)

	v := validation.NewStructuralValidator()
	verdict, err := v.ValidateBlock(ad, interfaces.DepthStructural)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Accept {
		t.Fatalf("a self-consistent real jmdn block must be approved, got reject: %s / %s",
			verdict.Reason, verdict.Detail)
	}
}

// TestParity_StructuralValidatorRejectsTamperedBlock proves the check has teeth
// against a real block: substitute one transaction AFTER the block hash/root
// were computed, and avc must reject — this is the body-substitution attack
// the check exists to catch.
func TestParity_StructuralValidatorRejectsTamperedBlock(t *testing.T) {
	txs := []config.Transaction{tx(0xAA), tx(0xBB), tx(0xCC)}
	blk := realBlock(txs)

	// Tamper: swap the last transaction for a different one, leaving the
	// (now-stale) BlockHash and TxnsRoot in place — exactly what a malicious
	// sequencer substituting a body would produce.
	blk.Transactions[2] = tx(0xDD)

	ad := adapters.NewZKBlockAdapter(blk)
	v := validation.NewStructuralValidator()
	verdict, err := v.ValidateBlock(ad, interfaces.DepthStructural)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("a tampered block (transaction substituted after hashing) MUST be rejected")
	}
}

// TestParity_TypedNilBlockIsRejectedNotPanic proves the classic typed-nil case
// is safe: a *ZKBlockAdapter wrapping a nil *config.ZKBlock is a non-nil
// interface value, so a naive == nil check would miss it and the first field
// access would panic. avc's IsNilBlock guard must catch it.
func TestParity_TypedNilBlockIsRejectedNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("typed-nil block must be rejected, not panic: %v", r)
		}
	}()
	ad := adapters.NewZKBlockAdapter(nil)
	v := validation.NewStructuralValidator()
	verdict, err := v.ValidateBlock(ad, interfaces.DepthStructural)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("a nil-backed block must never be approved")
	}
}
