package Security

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

var (
	nrgSender  = common.HexToAddress("0x1000000000000000000000000000000000000001")
	nrgFunded  = common.HexToAddress("0x2000000000000000000000000000000000000002")
	nrgFresh   = common.HexToAddress("0x3000000000000000000000000000000000000003")
	nrgDrained = common.HexToAddress("0x4000000000000000000000000000000000000004")
	nrgDIDOnly = common.HexToAddress("0x5000000000000000000000000000000000000005")
)

func nrgCache() *SecurityCache {
	c := NewSecurityCache()
	c.RegisterAccount(nrgSender, &DB_OPs.Account{Address: nrgSender, Balance: "1000000000000000000"})
	c.RegisterAccount(nrgFunded, &DB_OPs.Account{Address: nrgFunded, Balance: "1"})
	c.RegisterAccount(nrgDrained, &DB_OPs.Account{Address: nrgDrained, Balance: "0", TxNonce: 2})
	c.RegisterAccount(nrgDIDOnly, &DB_OPs.Account{Address: nrgDIDOnly, Balance: "0"})
	return c
}

func nrgTx(to *common.Address, gas uint64) *config.Transaction {
	return &config.Transaction{From: &nrgSender, To: to, GasLimit: gas, Value: big.NewInt(1)}
}

func TestCheckNewRecipientGas(t *testing.T) {
	const newErr = "gas limit 21000 below 46000 required for a new recipient"
	cases := []struct {
		name    string
		tx      *config.Transaction
		wantMsg string // "" = no error
	}{
		{"funded account at 21000", nrgTx(&nrgFunded, 21_000), ""},
		// An existing account drained to zero creates no new state.
		{"drained account at 21000", nrgTx(&nrgDrained, 21_000), ""},
		{"new address at 21000", nrgTx(&nrgFresh, 21_000), newErr},
		// A free DID registration does not make the address "used".
		{"registered but never used at 21000", nrgTx(&nrgDIDOnly, 21_000), newErr},
		{"registered but never used at 46000", nrgTx(&nrgDIDOnly, 46_000), ""},
		{"new address, gas limit 0", nrgTx(&nrgFresh, 0), newErr},
		{"new address at 45999", nrgTx(&nrgFresh, 45_999), "gas limit 45999 below 46000 required for a new recipient"},
		{"new address at 46000", nrgTx(&nrgFresh, 46_000), ""},
		{"contract creation", nrgTx(nil, 21_000), ""},
	}
	for _, c := range cases {
		err := checkNewRecipientGas(c.tx, nrgCache())
		if c.wantMsg == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || err.Error() != c.wantMsg {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantMsg)
		}
		if !errors.Is(err, config.ErrNewRecipientGas) {
			t.Errorf("%s: error does not match ErrNewRecipientGas", c.name)
		}
	}
}

// The activation gate at submission: off, before the height, from the height
// on (judged by the NEXT block), and an unreadable tip, which skips the rule.
func TestSubmissionNewRecipientGasGate(t *testing.T) {
	prev := chainTip
	t.Cleanup(func() {
		chainTip = prev
		config.SetNewRecipientGasFrom(0)
	})
	boom := errors.New("db down")
	cases := []struct {
		name      string
		from, tip uint64
		tipErr    error
		wantErr   bool
	}{
		{"rule off", 0, 500, nil, false},
		{"before activation", 100, 98, nil, false},
		{"next block activates", 100, 99, nil, true},
		{"well past activation", 100, 500, nil, true},
		{"tip unreadable: skipped", 100, 0, boom, false},
	}
	for _, c := range cases {
		config.SetNewRecipientGasFrom(c.from)
		tip, tipErr := c.tip, c.tipErr
		chainTip = func(context.Context) (uint64, error) { return tip, tipErr }
		err := checkSubmissionNewRecipientGas(context.Background(), nrgTx(&nrgFresh, 21_000), nrgCache())
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
	}
}
