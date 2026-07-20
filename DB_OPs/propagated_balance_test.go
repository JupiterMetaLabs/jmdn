package DB_OPs

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Tests for NormalizePropagatedAccountState — the shared policy that resets the
// volatile ledger fields (balance, tx counters) of an account received via DID
// propagation to their canonical initial values.
func TestNormalizePropagatedAccountState_ResetsVolatileFields(t *testing.T) {
	cases := []struct {
		name        string
		balance     string
		txNonce     uint64
		txCountSent uint64
		wantAdjusted bool
	}{
		{"canonical zero-string balance", "0", 0, 0, false},
		{"empty balance", "", 0, 0, false},
		{"positive balance", "1000000", 0, 0, true},
		{"one unit balance", "1", 0, 0, true},
		{"decimal balance", "1000.50", 0, 0, true},
		{"very large balance", "999999999999999999999999999", 0, 0, true},
		{"non-numeric balance", "not-a-number", 0, 0, true},
		{"negative balance", "-5", 0, 0, true},
		{"non-zero txNonce", "0", 42, 0, true},
		{"non-zero txCountSent", "0", 0, 7, true},
		{"all non-canonical", "500", 9, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc := &Account{
				Address:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
				Balance:     c.balance,
				TxNonce:     c.txNonce,
				TxCountSent: c.txCountSent,
			}
			got := NormalizePropagatedAccountState(acc)
			if got != c.wantAdjusted {
				t.Errorf("adjusted = %v, want %v", got, c.wantAdjusted)
			}
			// Post-condition: volatile fields are always canonicalized.
			if acc.Balance != "0" {
				t.Errorf("Balance not reset: got %q", acc.Balance)
			}
			if acc.TxNonce != 0 {
				t.Errorf("TxNonce not reset: got %d", acc.TxNonce)
			}
			if acc.TxCountSent != 0 {
				t.Errorf("TxCountSent not reset: got %d", acc.TxCountSent)
			}
		})
	}
}

// The ART identity Nonce must survive normalization (required for Fastsync ART
// routing), and a nil account must be handled safely.
func TestNormalizePropagatedAccountState_PreservesARTNonceAndHandlesNil(t *testing.T) {
	acc := &Account{
		Address: common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Balance: "12345",
		Nonce:   0xABCDEF, // ART routing nonce — must be preserved
	}
	NormalizePropagatedAccountState(acc)
	if acc.Nonce != 0xABCDEF {
		t.Errorf("ART Nonce must be preserved, got %d", acc.Nonce)
	}
	if got := NormalizePropagatedAccountState(nil); got != false {
		t.Errorf("nil account must return false, got %v", got)
	}
}
