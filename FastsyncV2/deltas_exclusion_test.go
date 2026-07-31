package FastsyncV2

// Tests for the marker-exclusion behavior of reconciliation delta
// computation, now owned by DB_OPs.ComputeBlockDeltas (the apply-time
// recompute used by DB_OPs.ApplyBlockRecon): transactions already applied by
// live block processing (tx_processed marker) must contribute NOTHING to the
// deltas — never applied twice — while unmarked txs in the same block get
// full deltas.

import (
	"math/big"
	"strings"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func reconTestBlock() *config.ZKBlock {
	from1 := common.HexToAddress("0x1000000000000000000000000000000000000001")
	to1 := common.HexToAddress("0x2000000000000000000000000000000000000002")
	from2 := common.HexToAddress("0x3000000000000000000000000000000000000003")
	to2 := common.HexToAddress("0x4000000000000000000000000000000000000004")
	coinbase := common.HexToAddress("0x5000000000000000000000000000000000000005")
	zkvm := common.HexToAddress("0x6000000000000000000000000000000000000006")

	return &config.ZKBlock{
		BlockNumber:  100,
		Timestamp:    1750000000,
		CoinbaseAddr: &coinbase,
		ZKVMAddr:     &zkvm,
		Transactions: []config.Transaction{
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
				Hash:     common.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000000002"),
				From:     &from2,
				To:       &to2,
				Value:    big.NewInt(2_000_000),
				Type:     2,
				Nonce:    0, // first-ever tx from this sender
				GasLimit: 21000,
				MaxFee:   big.NewInt(50_000_000_000),
			},
		},
	}
}

// Marked txs contribute nothing; unmarked txs in the same block get full deltas.
func TestComputeBlockDeltas_MarkerExclusion(t *testing.T) {
	blk := reconTestBlock()
	skip := map[string]bool{blk.Transactions[0].Hash.String(): true}

	deltas := DB_OPs.ComputeBlockDeltas(blk, skip)

	if d, ok := deltas[strings.ToLower(blk.Transactions[0].From.Hex())]; ok && d.BalanceDelta.Sign() != 0 {
		t.Fatalf("marked tx's sender received a delta: %s", d.BalanceDelta)
	}
	if _, ok := deltas[strings.ToLower(blk.Transactions[0].To.Hex())]; ok {
		t.Fatalf("marked tx's receiver must not appear in deltas")
	}
	d2 := deltas[strings.ToLower(blk.Transactions[1].From.Hex())]
	if d2 == nil || d2.BalanceDelta.Sign() >= 0 {
		t.Fatalf("unmarked tx's sender must have a negative delta, got %+v", d2)
	}
}

// A block's deltas must conserve value: sender debits equal receiver +
// coinbase-side + zkvm credits exactly (config.SplitFee never leaks a wei).
func TestComputeBlockDeltas_ConservesValue(t *testing.T) {
	blk := reconTestBlock()
	deltas := DB_OPs.ComputeBlockDeltas(blk, map[string]bool{})

	sum := new(big.Int)
	for _, d := range deltas {
		sum.Add(sum, d.BalanceDelta)
	}
	if sum.Sign() != 0 {
		t.Fatalf("block deltas do not conserve value: net %s", sum)
	}
}

// A sender whose only tx has nonce 0 must still get TxNonce 1 (first-touch
// tracking), matching what live execution stores (tx.Nonce + 1).
func TestComputeBlockDeltas_NonceZeroSender(t *testing.T) {
	blk := reconTestBlock()
	deltas := DB_OPs.ComputeBlockDeltas(blk, map[string]bool{})

	d := deltas[strings.ToLower(blk.Transactions[1].From.Hex())]
	if d == nil {
		t.Fatal("missing sender delta")
	}
	if d.TxNonce != 1 {
		t.Fatalf("nonce-0 sender TxNonce = %d, want 1", d.TxNonce)
	}
	if d.TxCountSent != 1 || !d.IsSender {
		t.Fatalf("sender bookkeeping wrong: %+v", d)
	}
}

// Skipping every tx yields an empty delta map (fully applied block is a no-op).
func TestComputeBlockDeltas_AllMarkedIsEmpty(t *testing.T) {
	blk := reconTestBlock()
	skip := map[string]bool{
		blk.Transactions[0].Hash.String(): true,
		blk.Transactions[1].Hash.String(): true,
	}
	deltas := DB_OPs.ComputeBlockDeltas(blk, skip)
	for a, d := range deltas {
		if d.BalanceDelta.Sign() != 0 || d.TxCountSent != 0 {
			t.Fatalf("fully-marked block produced a delta for %s: %+v", a, d)
		}
	}
}
