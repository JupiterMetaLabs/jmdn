package BlockProcessing

// D-67 regression: the rollback snapshot set (originalState) is built from
// affectedAccountsForBlock. It MUST include the fee-recipient addresses the
// reward split (config.SplitFee) credits — otherwise a rollback (the in-loop
// tx-failure path or the post-apply fingerprint path) restores senders /
// recipients / coinbase / zkvm but leaves the reward credits applied, and a
// re-delivery credits them a second time (a certain double-credit divergence).

import (
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func TestAffectedAccountsForBlock_IncludesFeeRecipients(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	coinbase := common.HexToAddress("0x3333333333333333333333333333333333333333")
	zkvm := common.HexToAddress("0x4444444444444444444444444444444444444444")
	r1 := common.HexToAddress("0xAAaA000000000000000000000000000000000001")
	r2 := common.HexToAddress("0xAAaA000000000000000000000000000000000002")
	r3 := common.HexToAddress("0xAAaA000000000000000000000000000000000003")

	blk := &config.ZKBlock{
		CoinbaseAddr: &coinbase,
		ZKVMAddr:     &zkvm,
		Transactions: []config.Transaction{{From: &from, To: &to}},
		FeeRecipients: []config.FeeRecipient{
			{Addr: r1, Weight: 1},
			{Addr: r2, Weight: 1},
			{Addr: r3, Weight: 1},
		},
	}

	got := affectedAccountsForBlock(blk)

	for _, want := range []common.Address{from, to, coinbase, zkvm, r1, r2, r3} {
		if !got[want] {
			t.Errorf("affectedAccountsForBlock missing %s — it would not be snapshotted, so a rollback could not restore it", want.Hex())
		}
	}
	// The three fee recipients specifically are the D-67 gap: without them in the
	// snapshot set, their reward credits survive a rollback and double-apply.
	if !got[r1] || !got[r2] || !got[r3] {
		t.Fatalf("fee recipients missing from rollback snapshot set (D-67 double-credit gap)")
	}
}

// A contract-creation tx (nil To) and a block with nil optional addresses must
// not panic, and must still snapshot the sender.
func TestAffectedAccountsForBlock_NilSafe(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	blk := &config.ZKBlock{
		Transactions: []config.Transaction{{From: &from, To: nil}},
	}
	got := affectedAccountsForBlock(blk)
	if !got[from] {
		t.Errorf("sender missing from affected set")
	}
	if len(got) != 1 {
		t.Errorf("expected only the sender (nil To/coinbase/zkvm/recipients), got %d entries", len(got))
	}
}
