package DB_OPs

import (
	"testing"

	"gossipnode/DB_OPs/store"

	"github.com/ethereum/go-ethereum/common"
)

func TestStorePropagatedAccount_NeverTouchesExisting(t *testing.T) {
	addr := common.HexToAddress("0x8888888888888888888888888888888888888888")
	h := seedHandle(t, addr, "5000", 1)
	h.accounts[addr.Hex()].Nonce = 7

	err := StorePropagatedAccount(nil, &Account{Address: addr, Balance: "0", Nonce: 0, DIDAddress: "did:x"})
	if err != nil {
		t.Fatal(err)
	}
	got := h.accounts[addr.Hex()]
	if got.Balance != "5000" || got.Nonce != 7 {
		t.Fatalf("existing account overwritten: balance=%s nonce=%d", got.Balance, got.Nonce)
	}
}

func TestStorePropagatedAccount_NewAccountCarriesNoValue(t *testing.T) {
	h := &memAccountsHandle{accounts: map[string]*store.Account{}}
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(nil) })

	addr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	if err := StorePropagatedAccount(nil, &Account{Address: addr, Balance: "1000000", Nonce: 3, DIDAddress: "did:y"}); err != nil {
		t.Fatal(err)
	}
	got := h.accounts[addr.Hex()]
	if got == nil || got.Balance != "0" || got.Nonce != 0 || got.DIDAddress != "did:y" {
		t.Fatalf("new propagated account = %+v, want zero balance/nonce with DID", got)
	}
}
