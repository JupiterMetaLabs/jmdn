package Security

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// C-03: CheckTransactionValues is the fail-closed value gate that stops a
// negative Value/gas field from reaching the balance arithmetic (where a
// negative amount inverts sender/receiver updates). These tests pin the exact
// accept/reject boundary the ingress (AllChecks) and remote-admission
// (validateRemoteBlock) paths rely on.
func TestCheckTransactionValues(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	mk := func(v, gp, mf, mpf *big.Int, typ uint8) *config.Transaction {
		return &config.Transaction{
			From: &addr, To: &addr, Type: typ,
			Value: v, GasPrice: gp, MaxFee: mf, MaxPriorityFee: mpf,
		}
	}
	pos := big.NewInt(100)
	neg := big.NewInt(-100)

	cases := []struct {
		name   string
		tx     *config.Transaction
		wantOK bool
	}{
		{"nil tx rejected", nil, false},
		{"positive value + gas ok", mk(pos, pos, nil, nil, 0), true},
		{"nil value ok (treated as zero)", mk(nil, pos, nil, nil, 0), true},
		{"negative value rejected", mk(neg, pos, nil, nil, 0), false},
		{"negative gas_price rejected", mk(pos, neg, nil, nil, 0), false},
		{"negative max_fee rejected", mk(pos, nil, neg, pos, 2), false},
		{"negative max_priority_fee rejected", mk(pos, nil, pos, neg, 2), false},
		{"tip>maxfee rejected", mk(pos, nil, big.NewInt(5), big.NewInt(6), 2), false},
		{"tip<=maxfee ok", mk(pos, nil, big.NewInt(6), big.NewInt(5), 2), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := CheckTransactionValues(tc.tx)
			if ok != tc.wantOK {
				t.Fatalf("CheckTransactionValues ok=%v want %v (err=%v)", ok, tc.wantOK, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("expected a non-nil error on rejection")
			}
		})
	}
}

// TestCheckTransactionValues_InversionWitness documents WHY a negative value is
// rejected: with balance b and value v=-100, the sender update b-(-100) credits
// the sender and the receiver update b+(-100) debits the receiver. The gate must
// reject before that arithmetic runs.
func TestCheckTransactionValues_InversionWitness(t *testing.T) {
	b := big.NewInt(1000)
	v := big.NewInt(-100)
	senderAfter := new(big.Int).Sub(b, v)   // 1000 - (-100) = 1100 (CREDIT — wrong)
	receiverAfter := new(big.Int).Add(b, v) // 1000 + (-100) = 900  (DEBIT — wrong)
	if senderAfter.Cmp(b) <= 0 || receiverAfter.Cmp(b) >= 0 {
		t.Fatalf("inversion witness broke: sender %s receiver %s", senderAfter, receiverAfter)
	}
	addr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := &config.Transaction{From: &addr, To: &addr, Value: v}
	if ok, _ := CheckTransactionValues(tx); ok {
		t.Fatalf("SECURITY (C-03): negative-value tx accepted by the value gate")
	}
}
