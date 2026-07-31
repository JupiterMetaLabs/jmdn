package DB_OPs

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func delta(bal int64, txNonce, txCount uint64) *BlockAccountDelta {
	return &BlockAccountDelta{
		BalanceDelta: big.NewInt(bal),
		TxNonce:      txNonce,
		TxCountSent:  txCount,
		IsSender:     txCount > 0,
	}
}

// A missing account is created from zero with defaults; balance = delta.
func TestApplyDeltaToAccount_NewAccount(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	doc, err := applyDeltaToAccount(nil, addr, delta(12345, 0, 0), 1_750_000_000_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balance != "12345" {
		t.Fatalf("balance = %s, want 12345", doc.Balance)
	}
	if doc.AccountType != "user" || doc.CreatedAt == 0 {
		t.Fatalf("new-account defaults not applied: %+v", doc)
	}
}

// Existing balances accumulate; identity fields are preserved; TxNonce is
// monotonic (an older candidate never lowers it); TxCountSent adds.
func TestApplyDeltaToAccount_MergeSemantics(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000ab")
	existing := &Account{
		Address:     addr,
		DIDAddress:  "did:jmdn:keep-me",
		AccountType: "publickey",
		Balance:     "1000",
		TxNonce:     9,
		TxCountSent: 4,
		Nonce:       0xBEEF, // ART identity — must never change here
		CreatedAt:   111,
	}
	doc, err := applyDeltaToAccount(existing, addr, delta(-300, 7, 2), 42)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balance != "700" {
		t.Fatalf("balance = %s, want 700", doc.Balance)
	}
	if doc.TxNonce != 9 {
		t.Fatalf("TxNonce lowered to %d; monotonic guard must keep 9", doc.TxNonce)
	}
	if doc.TxCountSent != 6 {
		t.Fatalf("TxCountSent = %d, want 6", doc.TxCountSent)
	}
	if doc.DIDAddress != "did:jmdn:keep-me" || doc.AccountType != "publickey" || doc.Nonce != 0xBEEF || doc.CreatedAt != 111 {
		t.Fatalf("identity fields not preserved: %+v", doc)
	}
	if existing.Balance != "1000" {
		t.Fatalf("input account mutated in place")
	}
}

// A higher TxNonce candidate advances the stored one.
func TestApplyDeltaToAccount_TxNonceAdvances(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000ac")
	existing := &Account{Address: addr, Balance: "0", TxNonce: 3}
	doc, err := applyDeltaToAccount(existing, addr, delta(0, 8, 1), 42)
	if err != nil {
		t.Fatal(err)
	}
	if doc.TxNonce != 8 {
		t.Fatalf("TxNonce = %d, want 8", doc.TxNonce)
	}
}

// A transiently negative result is written as-is (the range total converges);
// it must not error or clamp.
func TestApplyDeltaToAccount_NegativeTransient(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000ad")
	existing := &Account{Address: addr, Balance: "100"}
	doc, err := applyDeltaToAccount(existing, addr, delta(-250, 0, 0), 42)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balance != "-150" {
		t.Fatalf("balance = %s, want -150 (no clamping)", doc.Balance)
	}
}

// Empty stored balance is treated as zero, matching GetAccount semantics.
func TestApplyDeltaToAccount_EmptyBalance(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000ae")
	existing := &Account{Address: addr, Balance: ""}
	doc, err := applyDeltaToAccount(existing, addr, delta(5, 0, 0), 42)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balance != "5" {
		t.Fatalf("balance = %s, want 5", doc.Balance)
	}
}

func partitionTestBlock(nTx int) *config.ZKBlock {
	coinbase := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	zkvm := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	blk := &config.ZKBlock{BlockNumber: 7, Timestamp: 1000, CoinbaseAddr: &coinbase, ZKVMAddr: &zkvm}
	for i := 0; i < nTx; i++ {
		from := common.BytesToAddress([]byte{byte(i + 1), 0x01})
		to := common.BytesToAddress([]byte{byte(i + 1), 0x02})
		var h common.Hash
		h[30] = byte(i >> 8)
		h[31] = byte(i)
		blk.Transactions = append(blk.Transactions, config.Transaction{
			Hash: h, From: &from, To: &to, Value: big.NewInt(1), Nonce: uint64(i + 1),
			GasLimit: 21000, GasPrice: big.NewInt(1_000_000_000),
		})
	}
	return blk
}

// Every pending tx lands in exactly one group, in block order; applied txs in
// none; each group's projected ops respect the budget; a group is never empty.
func TestPartitionReconGroups_Invariants(t *testing.T) {
	blk := partitionTestBlock(50)
	pending := make(map[string]bool)
	for i := range blk.Transactions {
		if i%3 != 0 { // every third tx already applied
			pending[blk.Transactions[i].Hash.String()] = true
		}
	}

	const budget = 20
	groups := partitionReconGroups(blk, pending, budget)

	seen := map[string]int{}
	for _, g := range groups {
		if len(g) == 0 {
			t.Fatal("empty group")
		}
		accounts := map[string]bool{}
		for _, h := range g {
			seen[h]++
			if !pending[h] {
				t.Fatalf("non-pending tx %s grouped", h)
			}
		}
		for i := range blk.Transactions {
			tx := &blk.Transactions[i]
			for _, h := range g {
				if tx.Hash.String() == h {
					for _, a := range txTouchedAccounts(blk, tx) {
						accounts[a] = true
					}
				}
			}
		}
		if len(accounts)+len(g) > budget {
			t.Fatalf("group exceeds budget: %d accounts + %d markers > %d", len(accounts), len(g), budget)
		}
	}
	for h := range pending {
		if seen[h] != 1 {
			t.Fatalf("pending tx %s appears %d times across groups, want 1", h, seen[h])
		}
	}
}

// A budget smaller than a single tx's footprint still admits one tx per group
// (a lone tx must always be committable).
func TestPartitionReconGroups_TinyBudget(t *testing.T) {
	blk := partitionTestBlock(3)
	pending := map[string]bool{}
	for i := range blk.Transactions {
		pending[blk.Transactions[i].Hash.String()] = true
	}
	groups := partitionReconGroups(blk, pending, 1)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (one tx each)", len(groups))
	}
	for _, g := range groups {
		if len(g) != 1 {
			t.Fatalf("group size = %d, want 1", len(g))
		}
	}
}
