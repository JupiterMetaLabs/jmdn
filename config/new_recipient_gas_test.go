package config

import (
	"errors"
	"testing"
)

// TransferGas is defined as FallbackTxGasLimit, so they cannot drift; this
// pins the value itself.
func TestTransferGasIs21000(t *testing.T) {
	if got := TransferGas; got != 21_000 {
		t.Fatalf("TransferGas = %d, want 21000", got)
	}
}

func TestRequiredTransferGas(t *testing.T) {
	if got := RequiredTransferGas(true); got != 21_000 {
		t.Errorf("existing account: got %d, want 21000", got)
	}
	if got := RequiredTransferGas(false); got != 46_000 {
		t.Errorf("new account: got %d, want 46000", got)
	}
}

func TestCheckNewRecipientGas(t *testing.T) {
	cases := []struct {
		name    string
		limit   uint64
		exists  bool
		wantMsg string // "" = no error
	}{
		{"new at 21000", 21_000, false, "gas limit 21000 below 46000 required for a new recipient"},
		{"new at 45999", 45_999, false, "gas limit 45999 below 46000 required for a new recipient"},
		{"new at 46000", 46_000, false, ""},
		{"new, limit 0 falls back to 21000", 0, false, "gas limit 21000 below 46000 required for a new recipient"},
		{"existing at 21000", 21_000, true, ""},
		{"existing, limit 0", 0, true, ""},
	}
	for _, c := range cases {
		err := CheckNewRecipientGas(c.limit, c.exists)
		if c.wantMsg == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || err.Error() != c.wantMsg {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantMsg)
		}
		if !errors.Is(err, ErrNewRecipientGas) {
			t.Errorf("%s: error does not match ErrNewRecipientGas", c.name)
		}
	}
}

func TestNewRecipientGasActiveAt(t *testing.T) {
	t.Cleanup(func() { SetNewRecipientGasFrom(0) })

	SetNewRecipientGasFrom(0)
	if NewRecipientGasActiveAt(0) || NewRecipientGasActiveAt(1<<62) {
		t.Fatal("rule must be off when the height is 0")
	}
	SetNewRecipientGasFrom(100)
	if NewRecipientGasActiveAt(99) {
		t.Error("active below the activation height")
	}
	if !NewRecipientGasActiveAt(100) || !NewRecipientGasActiveAt(101) {
		t.Error("inactive at or above the activation height")
	}
}
