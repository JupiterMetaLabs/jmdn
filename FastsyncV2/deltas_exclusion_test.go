package FastsyncV2

// Tests for the F3 marker-exclusion path in delta computation (invariant I2):
// transactions already applied by live block processing (tx_processed marker)
// must contribute NOTHING to reconciliation deltas, while unmarked txs in the
// same block get full deltas (I1 — gap blocks are never skipped).

import (
	"math/big"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
)

func deltaTestBlock() *types.ZKBlock {
	from1 := common.HexToAddress("0x1000000000000000000000000000000000000001")
	to1 := common.HexToAddress("0x2000000000000000000000000000000000000002")
	from2 := common.HexToAddress("0x3000000000000000000000000000000000000003")
	to2 := common.HexToAddress("0x4000000000000000000000000000000000000004")
	coinbase := common.HexToAddress("0x5000000000000000000000000000000000000005")
	zkvm := common.HexToAddress("0x6000000000000000000000000000000000000006")

	return &types.ZKBlock{
		BlockNumber:  100,
		CoinbaseAddr: &coinbase,
		ZKVMAddr:     &zkvm,
		Transactions: []types.Transaction{
			{
				Hash:     common.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000000001"),
				From:     &from1,
				To:       &to1,
				Value:    big.NewInt(1_000_000),
				Type:     0,
				Nonce:    7,
				GasLimit: 21000,
				GasPrice: big.NewInt(35_000_000_000),
			},
			{
				Hash:     common.HexToHash("0xbbbb000000000000000000000000000000000000000000000000000000000002"),
				From:     &from2,
				To:       &to2,
				Value:    big.NewInt(2_000_000),
				Type:     0,
				Nonce:    3,
				GasLimit: 21000,
				GasPrice: big.NewInt(35_000_000_000),
			},
		},
	}
}

func TestApplyBlockDeltas_SkipsLiveAppliedTxs(t *testing.T) {
	blk := deltaTestBlock()
	tx1 := &blk.Transactions[0]
	tx2 := &blk.Transactions[1]

	// tx1 is marked live-applied; only tx2 may contribute deltas.
	skip := map[string]bool{tx1.Hash.String(): true}

	deltas := make(map[string]*types.AccountDelta)
	applyBlockDeltas(blk, deltas, skip)

	// I2: tx1's sender and receiver must be completely absent.
	if _, ok := deltas[strings.ToLower(tx1.From.Hex())]; ok {
		t.Error("skipped tx sender present in deltas — live-applied tx would be double-applied (I2)")
	}
	if _, ok := deltas[strings.ToLower(tx1.To.Hex())]; ok {
		t.Error("skipped tx receiver present in deltas (I2)")
	}

	// I1: tx2 (unmarked) must have full effects.
	sender2 := deltas[strings.ToLower(tx2.From.Hex())]
	if sender2 == nil {
		t.Fatal("unmarked tx sender missing from deltas — gap-block effects would be lost (I1)")
	}
	// sender delta = -(value + gasLimit*gasPrice)
	wantSender := new(big.Int).Neg(new(big.Int).Add(
		big.NewInt(2_000_000),
		new(big.Int).Mul(big.NewInt(21000), big.NewInt(35_000_000_000)),
	))
	if sender2.BalanceDelta.Cmp(wantSender) != 0 {
		t.Errorf("unmarked tx sender delta: got %s, want %s", sender2.BalanceDelta, wantSender)
	}
	receiver2 := deltas[strings.ToLower(tx2.To.Hex())]
	if receiver2 == nil || receiver2.BalanceDelta.Cmp(big.NewInt(2_000_000)) != 0 {
		t.Errorf("unmarked tx receiver delta wrong: %+v", receiver2)
	}

	// Coinbase/ZKVM must only earn tx2's gas (one tx worth, not two).
	gasFee := new(big.Int).Mul(big.NewInt(21000), big.NewInt(35_000_000_000))
	halfGas := new(big.Int).Div(gasFee, big.NewInt(2))
	remainder := new(big.Int).Mod(gasFee, big.NewInt(2))
	wantCoinbase := new(big.Int).Add(halfGas, remainder)

	coinbase := deltas[strings.ToLower(blk.CoinbaseAddr.Hex())]
	if coinbase == nil || coinbase.BalanceDelta.Cmp(wantCoinbase) != 0 {
		t.Errorf("coinbase must earn exactly ONE tx's gas share: %+v, want %s", coinbase, wantCoinbase)
	}
	zkvm := deltas[strings.ToLower(blk.ZKVMAddr.Hex())]
	if zkvm == nil || zkvm.BalanceDelta.Cmp(halfGas) != 0 {
		t.Errorf("zkvm must earn exactly ONE tx's gas share: %+v, want %s", zkvm, halfGas)
	}
}

func TestApplyBlockDeltas_EmptySkipSetAppliesEverything(t *testing.T) {
	blk := deltaTestBlock()
	deltas := make(map[string]*types.AccountDelta)
	applyBlockDeltas(blk, deltas, map[string]bool{})

	// Both senders, both receivers, coinbase, zkvm = 6 accounts.
	if len(deltas) != 6 {
		t.Fatalf("want 6 delta accounts with empty skip set, got %d", len(deltas))
	}

	// Coinbase earns gas from BOTH txs.
	gasFee := new(big.Int).Mul(big.NewInt(21000), big.NewInt(35_000_000_000))
	halfGas := new(big.Int).Div(gasFee, big.NewInt(2))
	remainder := new(big.Int).Mod(gasFee, big.NewInt(2))
	perTxCoinbase := new(big.Int).Add(halfGas, remainder)
	wantCoinbase := new(big.Int).Mul(perTxCoinbase, big.NewInt(2))

	coinbase := deltas[strings.ToLower(blk.CoinbaseAddr.Hex())]
	if coinbase == nil || coinbase.BalanceDelta.Cmp(wantCoinbase) != 0 {
		t.Errorf("coinbase with no skips: %+v, want %s", coinbase, wantCoinbase)
	}
}

func TestApplyBlockDeltas_AllSkippedYieldsNothing(t *testing.T) {
	// A fully live-applied block (e.g. re-scanned by catchup after live
	// processing) must contribute zero deltas — the H1 double-apply scenario.
	blk := deltaTestBlock()
	skip := map[string]bool{
		blk.Transactions[0].Hash.String(): true,
		blk.Transactions[1].Hash.String(): true,
	}
	deltas := make(map[string]*types.AccountDelta)
	applyBlockDeltas(blk, deltas, skip)
	if len(deltas) != 0 {
		t.Fatalf("fully live-applied block must yield zero deltas (I2/H1), got %d accounts", len(deltas))
	}
}
