package config

import (
	"errors"
	"math/big"
	"testing"
)

func TestRequiredTransferGas(t *testing.T) {
	cases := []struct {
		name string
		bal  *big.Int
		want uint64
	}{
		{"no account", nil, 46_000},
		{"zero balance", big.NewInt(0), 46_000},
		{"one wei", big.NewInt(1), 21_000},
		{"funded", new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), 21_000},
	}
	for _, c := range cases {
		if got := RequiredTransferGas(c.bal); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCheckUnfundedRecipientGas(t *testing.T) {
	cases := []struct {
		name    string
		limit   uint64
		bal     *big.Int
		wantErr bool
	}{
		{"unfunded at 21000", 21_000, nil, true},
		{"unfunded at 45999", 45_999, big.NewInt(0), true},
		{"unfunded at 46000", 46_000, big.NewInt(0), false},
		{"unfunded above", 60_000, nil, false},
		{"unfunded, limit 0 falls back to 21000", 0, nil, true},
		{"funded at 21000", 21_000, big.NewInt(5), false},
		{"funded, limit 0", 0, big.NewInt(5), false},
	}
	for _, c := range cases {
		err := CheckUnfundedRecipientGas(c.limit, c.bal)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
		if err != nil && !errors.Is(err, ErrUnfundedRecipientGas) {
			t.Errorf("%s: error does not wrap ErrUnfundedRecipientGas: %v", c.name, err)
		}
	}
}

func TestUnfundedRecipientGasActiveAt(t *testing.T) {
	defer SetUnfundedRecipientGasFrom(0)

	SetUnfundedRecipientGasFrom(0)
	if UnfundedRecipientGasActiveAt(0) || UnfundedRecipientGasActiveAt(1<<62) {
		t.Fatal("rule must be off when the height is 0")
	}

	SetUnfundedRecipientGasFrom(100)
	if UnfundedRecipientGasActiveAt(99) {
		t.Error("active below the activation height")
	}
	if !UnfundedRecipientGasActiveAt(100) || !UnfundedRecipientGasActiveAt(101) {
		t.Error("inactive at or above the activation height")
	}
}
