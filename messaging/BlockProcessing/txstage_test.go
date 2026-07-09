package BlockProcessing

import (
	"math/big"
	"testing"

	"gossipnode/DB_OPs"

	"github.com/ethereum/go-ethereum/common"
)

// TestTxStage_ReadThroughAndAccumulation pins the staging semantics: a later
// step of the SAME tx must observe earlier staged mutations (self-transfer,
// sender==coinbase, ...), exactly as it did under the earlier sequential
// commits.
func TestTxStage_ReadThroughAndAccumulation(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	stage := newTxStage(nil) // nil conn: DB fallback must not be reached

	// Step 1: stage a deduction result.
	stage.put(&DB_OPs.Account{Address: addr, Balance: "900", TxNonce: 5})

	// Step 2 (same account, e.g. sender==coinbase): must see the staged doc,
	// not the committed DB state.
	doc, err := stage.get(addr)
	if err != nil {
		t.Fatalf("read-through get: %v", err)
	}
	if doc.Balance != "900" || doc.TxNonce != 5 {
		t.Fatalf("read-through returned stale state: %+v", doc)
	}

	// Accumulate a credit on the staged doc.
	bal, _ := new(big.Int).SetString(doc.Balance, 10)
	doc.Balance = new(big.Int).Add(bal, big.NewInt(50)).String()
	stage.put(doc)

	final, _ := stage.get(addr)
	if final.Balance != "950" {
		t.Fatalf("accumulation lost: %s", final.Balance)
	}

	// One account touched twice = ONE staged document (one ExecAll op).
	if got := len(stage.staged()); got != 1 {
		t.Fatalf("same address staged %d times, want 1", got)
	}
}

// TestTxStage_FirstTouchOrder pins deterministic ExecAll op ordering.
func TestTxStage_FirstTouchOrder(t *testing.T) {
	a := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	c := common.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")

	stage := newTxStage(nil)
	stage.put(&DB_OPs.Account{Address: b})
	stage.put(&DB_OPs.Account{Address: a})
	stage.put(&DB_OPs.Account{Address: c})
	stage.put(&DB_OPs.Account{Address: a}) // re-touch must not reorder

	got := stage.staged()
	want := []common.Address{b, a, c}
	if len(got) != 3 {
		t.Fatalf("staged %d docs, want 3", len(got))
	}
	for i, doc := range got {
		if doc.Address != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, doc.Address.Hex(), want[i].Hex())
		}
	}
}
