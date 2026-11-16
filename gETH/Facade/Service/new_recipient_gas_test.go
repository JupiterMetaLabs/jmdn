package Service

import (
	"context"
	"errors"
	"testing"

	"gossipnode/config"
)

// stubNewRecipientLookups replaces the tip and account lookups for one test.
func stubNewRecipientLookups(t *testing.T, from uint64, tip uint64, tipErr error, exists bool, existsErr error) {
	t.Helper()
	prevTip, prevUsed := estimateChainTip, estimateAccountUsed
	t.Cleanup(func() {
		estimateChainTip, estimateAccountUsed = prevTip, prevUsed
		config.SetNewRecipientGasFrom(0)
	})
	config.SetNewRecipientGasFrom(from)
	estimateChainTip = func(context.Context) (uint64, error) { return tip, tipErr }
	estimateAccountUsed = func(string) (bool, error) { return exists, existsErr }
}

func TestNewRecipientExtraGas(t *testing.T) {
	const to = "0x3000000000000000000000000000000000000003"
	boom := errors.New("db down")
	cases := []struct {
		name      string
		from, tip uint64
		tipErr    error
		exists    bool
		existsErr error
		want      uint64
	}{
		{"rule off", 0, 500, nil, false, nil, 0},
		{"before activation", 100, 98, nil, false, nil, 0},         // next block 99
		{"next block activates", 100, 99, nil, false, nil, 25_000}, // next block 100
		{"active, used account", 100, 500, nil, true, nil, 0},
		{"active, new or unused account", 100, 500, nil, false, nil, 25_000},
		{"tip unreadable: not quoted, as submission skips it", 100, 0, boom, false, nil, 0},
		{"account unreadable: assume new", 100, 500, nil, false, boom, 25_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubNewRecipientLookups(t, c.from, c.tip, c.tipErr, c.exists, c.existsErr)
			if got := newRecipientExtraGas(context.Background(), to); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}
