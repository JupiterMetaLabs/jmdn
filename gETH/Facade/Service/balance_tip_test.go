package Service

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// A concrete past block whose position against the tip cannot be read is an
// error, never the head balance ("finalized" resolves to such a block).
func TestBalance_UnreadableTipIsAnError(t *testing.T) {
	prevTip, prevHist := latestBlockNumberFn, balanceAtBlockFn
	t.Cleanup(func() { latestBlockNumberFn, balanceAtBlockFn = prevTip, prevHist })
	latestBlockNumberFn = func(context.Context) (uint64, error) { return 0, errors.New("tip unavailable") }
	balanceAtBlockFn = func(common.Address, uint64) (*big.Int, error) {
		t.Fatal("history must not be read without the tip")
		return nil, nil
	}
	s := &ServiceImpl{}
	if bal, err := s.Balance(context.Background(), "0x0000000000000000000000000000000000000001", big.NewInt(8), ""); err == nil {
		t.Fatalf("got balance %v for block 8 with an unreadable tip; want an error", bal)
	}
}

// A past block is answered from history.
func TestBalance_PastBlockUsesHistory(t *testing.T) {
	prevTip, prevHist := latestBlockNumberFn, balanceAtBlockFn
	t.Cleanup(func() { latestBlockNumberFn, balanceAtBlockFn = prevTip, prevHist })
	latestBlockNumberFn = func(context.Context) (uint64, error) { return 10, nil }
	var asked uint64
	balanceAtBlockFn = func(_ common.Address, n uint64) (*big.Int, error) {
		asked = n
		return big.NewInt(42), nil
	}
	s := &ServiceImpl{}
	bal, err := s.Balance(context.Background(), "0x0000000000000000000000000000000000000001", big.NewInt(8), "")
	if err != nil || bal.Int64() != 42 || asked != 8 {
		t.Fatalf("got %v, %v (history asked for %d); want 42 from block 8", bal, err, asked)
	}
}
