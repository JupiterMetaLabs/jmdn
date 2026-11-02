//go:build applygate

package BlockProcessing_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/messaging/BlockProcessing"
)

// Accounts that do not exist at genesis. acctC is created by the first block
// and then sends in the same block; feeX and feeY are never-funded fee
// recipients created by their first credit.
var (
	acctC = common.HexToAddress("0x00000000000000000000000000000000000000C3")
	acctD = common.HexToAddress("0x00000000000000000000000000000000000000D4")
	feeX  = common.HexToAddress("0x00000000000000000000000000000000000000E5")
	feeY  = common.HexToAddress("0x00000000000000000000000000000000000000F6")
)

type accountState struct {
	Balance     string
	TxNonce     uint64
	TxCountSent uint64
	Nonce       uint64
}

// makeFeeBlock is makeBlock with fee recipients set before the identity
// nonces are stamped, as the sequencer does.
func makeFeeBlock(t *testing.T, num uint64, prev common.Hash, txs []config.Transaction, fees []config.FeeRecipient) *config.ZKBlock {
	t.Helper()
	cb, zk := acctA, acctB
	blk := &config.ZKBlock{
		Transactions:  txs,
		Timestamp:     int64(1_700_000_000 + num),
		CoinbaseAddr:  &cb,
		ZKVMAddr:      &zk,
		FeeRecipients: fees,
		PrevHash:      prev,
		BlockNumber:   num,
		GasLimit:      30_000_000,
	}
	blk.BlockHash = Security.RecomputeBlockHashFromContents(txs)
	if err := DB_OPs.EnrichBlockAccountNoncesWithPredicted(blk, nil); err != nil {
		t.Fatalf("enrich block %d: %v", num, err)
	}
	return blk
}

func transfer(from, to common.Address, nonce uint64, wei int64) config.Transaction {
	return callTx(from, to, nonce, nil, big.NewInt(wei))
}

// groupedBlocks builds and applies, on the installed store, two blocks that
// stress a shared stage: repeated senders, an account created and then
// spending in one block, and fee recipients created by their first credit and
// credited by every tx after.
func groupedBlocks(t *testing.T) []*config.ZKBlock {
	t.Helper()
	fees := []config.FeeRecipient{{Addr: feeX, Weight: 3}, {Addr: feeY, Weight: 1}, {Addr: acctA, Weight: 2}}
	const eth = int64(1e18)

	b1 := makeFeeBlock(t, 1, common.Hash{}, []config.Transaction{
		transfer(acctA, acctC, 0, 5*eth),
		transfer(acctA, acctB, 1, 1*eth),
		transfer(acctC, acctD, 0, 2*eth), // acctC exists only in the stage here
		transfer(acctB, acctC, 0, eth/2),
		transfer(acctC, acctA, 1, eth/4),
		transfer(acctD, acctB, 0, eth/8), // acctD created two txs earlier
	}, fees)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("apply block 1: %v", err)
	}
	b2 := makeFeeBlock(t, 2, b1.BlockHash, []config.Transaction{
		transfer(acctC, acctB, 2, eth/10),
		transfer(acctA, acctD, 2, 3*eth),
		transfer(acctD, acctC, 1, eth),
		transfer(acctB, acctA, 1, eth/3),
	}, fees)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b2, nil); err != nil {
		t.Fatalf("apply block 2: %v", err)
	}
	if b1.StateFingerprint == "" || b2.StateFingerprint == "" {
		t.Fatal("blocks were not stamped with a state fingerprint")
	}
	return []*config.ZKBlock{b1, b2}
}

func captureAccounts(t *testing.T) map[common.Address]accountState {
	t.Helper()
	out := map[common.Address]accountState{}
	for _, a := range []common.Address{acctA, acctB, acctC, acctD, feeX, feeY} {
		d, err := DB_OPs.GetAccount(nil, a)
		if err != nil {
			t.Fatalf("GetAccount(%s): %v", a.Hex(), err)
		}
		out[a] = accountState{d.Balance, d.TxNonce, d.TxCountSent, d.Nonce}
	}
	return out
}

func markersApplied(t *testing.T, blocks []*config.ZKBlock) {
	t.Helper()
	var hashes []string
	for _, b := range blocks {
		for _, tx := range b.Transactions {
			hashes = append(hashes, tx.Hash.String())
		}
	}
	got, err := DB_OPs.FilterProcessedTxMarkers(hashes)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hashes {
		if !got[h] {
			t.Fatalf("tx %s has no applied marker", h)
		}
	}
}

// replay applies stamped blocks on a fresh store; ProcessBlockTransactions
// verifies each block's state fingerprint and fails on any difference.
func replay(t *testing.T, blocks []*config.ZKBlock) map[common.Address]accountState {
	t.Helper()
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	for _, b := range blocks {
		if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b, nil); err != nil {
			t.Fatalf("replay block %d: %v", b.BlockNumber, err)
		}
	}
	markersApplied(t, blocks)
	return captureAccounts(t)
}

func produce(t *testing.T, grouped bool, budget int) ([]*config.ZKBlock, map[common.Address]accountState) {
	t.Helper()
	restore := BlockProcessing.SetGroupedApplyForTest(grouped, budget)
	defer restore()
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	blocks := groupedBlocks(t)
	markersApplied(t, blocks)
	return blocks, captureAccounts(t)
}

func sameAccounts(t *testing.T, label string, want, got map[common.Address]accountState) {
	t.Helper()
	for a, w := range want {
		if got[a] != w {
			t.Fatalf("%s: account %s = %+v, want %+v", label, a.Hex(), got[a], w)
		}
	}
}

// Grouped and per-tx commits must give identical state, block fingerprints
// and markers, in both directions and with a group split by the op budget.
func TestApplyGate_GroupedCommitMatchesPerTx(t *testing.T) {
	perTxBlocks, perTx := produce(t, false, 0)

	// Grouped replay of per-tx-stamped blocks.
	func() {
		restore := BlockProcessing.SetGroupedApplyForTest(true, 0)
		defer restore()
		sameAccounts(t, "grouped replay", perTx, replay(t, perTxBlocks))
	}()

	// Grouped producer, per-tx replay.
	groupedBlocksOut, grouped := produce(t, true, 0)
	sameAccounts(t, "grouped producer", perTx, grouped)
	for i := range groupedBlocksOut {
		if groupedBlocksOut[i].StateFingerprint != perTxBlocks[i].StateFingerprint {
			t.Fatalf("block %d fingerprint differs: grouped %s per-tx %s", i+1,
				groupedBlocksOut[i].StateFingerprint, perTxBlocks[i].StateFingerprint)
		}
	}
	func() {
		restore := BlockProcessing.SetGroupedApplyForTest(false, 0)
		defer restore()
		sameAccounts(t, "per-tx replay", perTx, replay(t, groupedBlocksOut))
	}()

	// A small budget splits each block into several groups.
	_, split := produce(t, true, 6)
	sameAccounts(t, "budget-split groups", perTx, split)
}

// A tx that fails in the middle of an open group rolls back the whole block:
// no account changes and no applied markers, including for the group's
// earlier txs that were staged but never committed.
func TestApplyGate_GroupedFailureRollsBackBlock(t *testing.T) {
	restore := BlockProcessing.SetGroupedApplyForTest(true, 0)
	defer restore()
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)

	pre := map[common.Address]accountState{}
	for _, a := range []common.Address{acctA, acctB} {
		d, err := DB_OPs.GetAccount(nil, a)
		if err != nil {
			t.Fatal(err)
		}
		pre[a] = accountState{d.Balance, d.TxNonce, d.TxCountSent, d.Nonce}
	}

	const eth = int64(1e18)
	ok1 := transfer(acctA, acctB, 0, eth)
	ok2 := transfer(acctB, acctA, 0, eth)
	// Stale nonce: acctA's nonce 0 was used by ok1, which is only staged
	// (not stored) when this tx runs, so this also checks the nonce is read
	// from the group's stage.
	bad := transfer(acctA, acctD, 0, eth)
	blk := makeBlock(t, 1, common.Hash{}, []config.Transaction{ok1, ok2, bad})
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), blk, nil); err == nil {
		t.Fatal("expected the block to fail on the bad nonce")
	}

	for a, want := range pre {
		d, err := DB_OPs.GetAccount(nil, a)
		if err != nil {
			t.Fatal(err)
		}
		if got := (accountState{d.Balance, d.TxNonce, d.TxCountSent, d.Nonce}); got != want {
			t.Fatalf("account %s changed by a failed block: %+v, want %+v", a.Hex(), got, want)
		}
	}
	applied, err := DB_OPs.FilterProcessedTxMarkers([]string{ok1.Hash.String(), ok2.Hash.String(), bad.Hash.String()})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("markers applied after a failed block: %v", applied)
	}

	// The lock was released: a valid block applies next.
	good := makeBlock(t, 1, common.Hash{}, []config.Transaction{ok1, ok2})
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), good, nil); err != nil {
		t.Fatalf("clean apply after failure: %v", err)
	}
}
