package DB_OPs

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Regression guard for the account-sync balance-clobber divergence class:
// a placeholder/sync write carrying Balance "0" must never overwrite a real
// stored balance, even when it wins last-writer-wins on timestamp. Balance-
// bearing writers (live execution, reconciliation) bypass this merge; the
// writes that reach it (account-sync, restore, DID propagation) carry "0".

func TestMergeAccountForWrite_ZeroDoesNotClobberBalance(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	existing := &Account{Address: addr, Balance: "1000", UpdatedAt: 100, TxNonce: 5, TxCountSent: 3, AccountType: "user"}

	// Incoming sync write: Balance "0", NEWER timestamp → wins LWW.
	got, changed := mergeAccountForWrite(existing, Account{Address: addr, Balance: "0", UpdatedAt: 200})
	if got.Balance != "1000" {
		t.Fatalf("zero sync write clobbered real balance: got %q, want %q (changed=%v)", got.Balance, "1000", changed)
	}

	// Empty-string balance is also "no value" → preserved.
	got2, _ := mergeAccountForWrite(existing, Account{Address: addr, Balance: "", UpdatedAt: 300})
	if got2.Balance != "1000" {
		t.Fatalf("empty sync write clobbered real balance: got %q, want %q", got2.Balance, "1000")
	}
}

func TestMergeAccountForWrite_RealBalanceStillApplies(t *testing.T) {
	addr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	existing := &Account{Address: addr, Balance: "1000", UpdatedAt: 100}
	got, changed := mergeAccountForWrite(existing, Account{Address: addr, Balance: "2500", UpdatedAt: 200})
	if !changed || got.Balance != "2500" {
		t.Fatalf("real balance update did not apply: got %q changed=%v, want %q true", got.Balance, changed, "2500")
	}
}

func TestMergeAccountForWrite_NewZeroAccountInserted(t *testing.T) {
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	got, changed := mergeAccountForWrite(nil, Account{Address: addr, Balance: "0", UpdatedAt: 200})
	if !changed || got.Balance != "0" {
		t.Fatalf("new zero account not inserted verbatim: got %q changed=%v", got.Balance, changed)
	}
}

func TestMergeAccountForWrite_StaleZeroLosesLWW(t *testing.T) {
	addr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	existing := &Account{Address: addr, Balance: "1000", UpdatedAt: 300}
	// Older timestamp → loses LWW → not applied (no write); real balance safe.
	if _, changed := mergeAccountForWrite(existing, Account{Address: addr, Balance: "0", UpdatedAt: 100}); changed {
		t.Fatalf("stale zero write should not be applied (LWW), changed=true")
	}
}
