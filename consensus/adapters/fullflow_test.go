package adapters

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/JupiterMetaLabs/avc/validation"

	"gossipnode/config"
)

// These tests prove the ADAPTER correctly carries a real jmdn block through
// the COMPLETE validation flow (Phase 1 stateless → structural → Phase 2
// stateful), and that every transaction's bytes arrive at each phase intact
// and in block order.
//
// IMPORTANT SCOPE: the stateless/stateful checkers here are STUBS. They prove
// the PLUMBING — that jmdn's data reaches each phase correctly — NOT the real
// signature/balance/nonce logic, which jmdn has not implemented yet (those are
// interfaces.StatelessTxChecker / StatefulTxChecker, injected in a later step).
// So: "Phase 1 & 2 receive and process jmdn's transactions correctly" is
// PROVEN here. "Phase 1 & 2 actually verify signatures/balances" is NOT — no
// real checker exists to test yet.

// recordingStateless records the hex hash of every tx it is asked to check,
// so the test can prove the adapter delivered exactly the block's transactions.
type recordingStateless struct {
	seen    []string
	failAll bool
}

func (c *recordingStateless) CheckTx(_ context.Context, tx interfaces.Transaction) error {
	c.seen = append(c.seen, hex.EncodeToString(tx.TxHashBytes()))
	if c.failAll {
		return errors.New("stub stateless failure")
	}
	return nil
}

// recordingStateful records the order it applied transactions in, proving
// Phase 2 receives them strictly in block order through the adapter.
type recordingStateful struct {
	applied []string
	failAll bool
}

func (c *recordingStateful) CheckAndApply(_ context.Context, tx interfaces.Transaction) error {
	c.applied = append(c.applied, hex.EncodeToString(tx.TxHashBytes()))
	if c.failAll {
		return errors.New("stub stateful failure")
	}
	return nil
}

// wantHashes returns the hex tx hashes of a config transaction slice in order.
func wantHashes(txs []config.Transaction) []string {
	out := make([]string, len(txs))
	for i := range txs {
		out[i] = hex.EncodeToString(txs[i].Hash.Bytes())
	}
	return out
}

func equalOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFullFlow_AdapterFeedsAllThreeStages: a self-consistent jmdn block, fed
// through the adapter into avc's FullValidator at DepthFull, is APPROVED, and
// every transaction reaches both Phase 1 and Phase 2 exactly once, in order.
func TestFullFlow_AdapterFeedsAllThreeStages(t *testing.T) {
	txs := []config.Transaction{tx(0xAA), tx(0xBB), tx(0xCC)}
	blk := realBlock(txs)
	ad := NewZKBlockAdapter(blk)

	stateless := &recordingStateless{}
	stateful := &recordingStateful{}
	v := validation.NewFullValidator(stateless, stateful, 0)

	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Accept {
		t.Fatalf("self-consistent real block must be approved at DepthFull, got reject: %s / %s",
			verdict.Reason, verdict.Detail)
	}

	want := wantHashes(txs)
	// Phase 1 must have seen every transaction (order not required — it's
	// parallel — so compare as sets by length + membership via ordered compare
	// after avc's deterministic feed; here we just assert count + membership).
	if len(stateless.seen) != len(want) {
		t.Fatalf("Phase 1 saw %d txs, want %d — adapter did not deliver all transactions",
			len(stateless.seen), len(want))
	}
	seenSet := map[string]bool{}
	for _, h := range stateless.seen {
		seenSet[h] = true
	}
	for _, h := range want {
		if !seenSet[h] {
			t.Fatalf("Phase 1 never received tx %s — adapter dropped or mangled a transaction", h)
		}
	}
	// Phase 2 is serial and MUST receive them in exact block order.
	if !equalOrdered(stateful.applied, want) {
		t.Fatalf("Phase 2 order/contents wrong:\n got  %v\n want %v", stateful.applied, want)
	}
}

// TestFullFlow_Phase1FailureVetoesBeforePhase2: proves a Phase 1 rejection
// stops the flow — Phase 2 (the mutating stateful stage) never runs.
func TestFullFlow_Phase1FailureVetoesBeforePhase2(t *testing.T) {
	blk := realBlock([]config.Transaction{tx(0xAA), tx(0xBB)})
	ad := NewZKBlockAdapter(blk)

	stateless := &recordingStateless{failAll: true}
	stateful := &recordingStateful{}
	v := validation.NewFullValidator(stateless, stateful, 0)

	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("Phase 1 failure must veto")
	}
	if verdict.Reason != interfaces.ReasonStatelessCheckFailed {
		t.Fatalf("wrong reason: got %s, want %s", verdict.Reason, interfaces.ReasonStatelessCheckFailed)
	}
	if len(stateful.applied) != 0 {
		t.Fatalf("Phase 2 must not run after Phase 1 failed, but it applied %d txs", len(stateful.applied))
	}
}

// TestFullFlow_Phase2FailureVetoes: Phase 1 passes, Phase 2 rejects → block
// vetoed with the stateful reason.
func TestFullFlow_Phase2FailureVetoes(t *testing.T) {
	blk := realBlock([]config.Transaction{tx(0xAA), tx(0xBB)})
	ad := NewZKBlockAdapter(blk)

	v := validation.NewFullValidator(&recordingStateless{}, &recordingStateful{failAll: true}, 0)

	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("Phase 2 failure must veto")
	}
	if verdict.Reason != interfaces.ReasonStatefulCheckFailed {
		t.Fatalf("wrong reason: got %s, want %s", verdict.Reason, interfaces.ReasonStatefulCheckFailed)
	}
}

// TestFullFlow_NilCheckersFailClosed: DepthFull with no real checkers injected
// (jmdn's current state) must REJECT, not silently approve or fall back to
// structural-only. This is the correct behaviour until jmdn supplies checkers.
func TestFullFlow_NilCheckersFailClosed(t *testing.T) {
	blk := realBlock([]config.Transaction{tx(0xAA)})
	ad := NewZKBlockAdapter(blk)

	v := validation.NewFullValidator(nil, nil, 0)
	verdict, err := v.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Accept {
		t.Fatal("DepthFull with no checkers injected must fail closed (reject), not approve")
	}
}
