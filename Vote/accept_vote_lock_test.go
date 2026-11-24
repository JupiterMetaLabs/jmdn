package Vote

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

func TestReserveAcceptVote(t *testing.T) {
	prev := roundlock.Votes
	roundlock.Votes = roundlock.NewLedger()
	t.Cleanup(func() { roundlock.Votes = prev })

	blk := func(hash string, period uint64) *config.ZKBlock {
		return &config.ZKBlock{BlockNumber: 8, Period: period, BlockHash: common.HexToHash(hash)}
	}
	if err := reserveAcceptVote(blk("0xa", 0)); err != nil {
		t.Fatal(err)
	}
	if err := reserveAcceptVote(blk("0xa", 0)); err != nil {
		t.Fatal("re-voting the same block must be allowed")
	}
	if err := reserveAcceptVote(blk("0xb", 0)); err == nil {
		t.Fatal("a second block at the same round was accepted")
	}
	if err := reserveAcceptVote(blk("0xb", 1)); err != nil {
		t.Fatal("the next period must be open")
	}
	if err := reserveAcceptVote(blk("0xc", 0)); err == nil {
		t.Fatal("a lower period than one already accepted was accepted")
	}
}
