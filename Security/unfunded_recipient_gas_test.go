package Security

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

func TestCheckUnfundedRecipientGasAtSubmission(t *testing.T) {
	sender := common.HexToAddress("0x1000000000000000000000000000000000000001")
	funded := common.HexToAddress("0x2000000000000000000000000000000000000002")
	fresh := common.HexToAddress("0x3000000000000000000000000000000000000003")
	empty := common.HexToAddress("0x4000000000000000000000000000000000000004")

	cache := NewSecurityCache()
	cache.RegisterAccount(sender, &DB_OPs.Account{Address: sender, Balance: "1000000000000000000"})
	cache.RegisterAccount(funded, &DB_OPs.Account{Address: funded, Balance: "1"})
	cache.RegisterAccount(empty, &DB_OPs.Account{Address: empty, Balance: "0"})

	tx := func(to *common.Address, gas uint64) *config.Transaction {
		return &config.Transaction{From: &sender, To: to, GasLimit: gas, Value: big.NewInt(1)}
	}

	cases := []struct {
		name    string
		tx      *config.Transaction
		wantMsg string // "" = no error
	}{
		{"funded recipient at 21000", tx(&funded, 21_000), ""},
		{"new address at 21000", tx(&fresh, 21_000), "gas limit 21000 below 46000 required for an unfunded recipient"},
		{"zero-balance account at 21000", tx(&empty, 21_000), "gas limit 21000 below 46000 required for an unfunded recipient"},
		{"new address, gas limit 0", tx(&fresh, 0), "gas limit 21000 below 46000 required for an unfunded recipient"},
		{"new address at 45999", tx(&fresh, 45_999), "gas limit 45999 below 46000 required for an unfunded recipient"},
		{"new address at 46000", tx(&fresh, 46_000), ""},
		{"contract creation", tx(nil, 21_000), ""},
	}
	for _, c := range cases {
		err := checkUnfundedRecipientGas(c.tx, cache)
		if c.wantMsg == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || err.Error() != c.wantMsg {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantMsg)
		}
		if !errors.Is(err, config.ErrUnfundedRecipientGas) {
			t.Errorf("%s: error does not match ErrUnfundedRecipientGas", c.name)
		}
	}
}

func TestSubmissionUnfundedRecipientGasOff(t *testing.T) {
	config.SetUnfundedRecipientGasFrom(0)
	sender := common.HexToAddress("0x1000000000000000000000000000000000000001")
	fresh := common.HexToAddress("0x3000000000000000000000000000000000000003")
	tx := &config.Transaction{From: &sender, To: &fresh, GasLimit: 21_000, Value: big.NewInt(1)}
	if err := checkSubmissionUnfundedRecipientGas(t.Context(), tx, NewSecurityCache()); err != nil {
		t.Fatalf("rule must be off when the height is 0: %v", err)
	}
}
