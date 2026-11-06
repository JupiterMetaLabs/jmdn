package Service

import (
	"context"
	"math"
	"math/big"
	"strings"
	"testing"
)

// A public caller picks blockCount. A huge one must be served as the newest
// maxFeeHistoryBlocks blocks, not allocated in full: before the cap, a request
// for 1<<22 blocks made the node allocate hundreds of MiB, and 1<<63 made the
// block arithmetic wrap and the allocation panic.
func TestFeeHistory_CapsBlockCount(t *testing.T) {
	s := &ServiceImpl{}
	newest := big.NewInt(10_000_000)
	for _, n := range []uint64{maxFeeHistoryBlocks + 1, 1 << 22, 1 << 63, math.MaxUint64} {
		got, err := s.FeeHistory(context.Background(), n, newest, []float64{10, 50, 90})
		if err != nil {
			t.Fatalf("blockCount %d: %v", n, err)
		}
		fees := got["baseFeePerGas"].([]string)
		ratios := got["gasUsedRatio"].([]float64)
		rewards := got["reward"].([][]string)
		// The newest maxFeeHistoryBlocks blocks; one more base fee, for the
		// next block.
		if len(ratios) != maxFeeHistoryBlocks || len(rewards) != maxFeeHistoryBlocks || len(fees) != maxFeeHistoryBlocks+1 {
			t.Fatalf("blockCount %d: got %d fees, %d ratios, %d rewards; want %d, %d, %d",
				n, len(fees), len(ratios), len(rewards), maxFeeHistoryBlocks+1, maxFeeHistoryBlocks, maxFeeHistoryBlocks)
		}
		if got["oldestBlock"] != "0x989281" { // 10,000,000 - 1024 + 1
			t.Fatalf("blockCount %d: oldestBlock = %v, want 0x989281", n, got["oldestBlock"])
		}
	}
}

// The reply size is newest - oldest + 1, so the blockCount cap alone does not
// bound it: a negative newest became a huge count through Uint64, and -2^63
// panicked in make. Any newest that is not a uint64 height is refused.
func TestFeeHistory_RejectsInvalidNewest(t *testing.T) {
	s := &ServiceImpl{}
	tooBig := new(big.Int).Lsh(big.NewInt(1), 64)
	for _, newest := range []*big.Int{
		big.NewInt(-5),
		big.NewInt(-1 << 30),
		big.NewInt(math.MinInt64),
		tooBig,
	} {
		got, err := s.FeeHistory(context.Background(), 10, newest, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid newest block") {
			t.Fatalf("newest %s: want an invalid newest block error, got %v (%d entries)",
				newest, err, len(got["baseFeePerGas"].([]string)))
		}
	}
}

// A request within the cap has geth's layout: blocks 97..100 for blockCount 4
// at newest 100, one ratio and one reward row per block, one more base fee.
func TestFeeHistory_GethLayout(t *testing.T) {
	s := &ServiceImpl{}
	got, err := s.FeeHistory(context.Background(), 4, big.NewInt(100), nil)
	if err != nil {
		t.Fatal(err)
	}
	if fees := got["baseFeePerGas"].([]string); len(fees) != 5 {
		t.Fatalf("got %d fees, want 5", len(fees))
	}
	if ratios := got["gasUsedRatio"].([]float64); len(ratios) != 4 {
		t.Fatalf("got %d ratios, want 4", len(ratios))
	}
	if got["oldestBlock"] != "0x61" {
		t.Fatalf("oldestBlock = %v, want 0x61", got["oldestBlock"])
	}
	if _, ok := got["reward"]; ok {
		t.Fatal("reward present without percentiles")
	}

	got, err = s.FeeHistory(context.Background(), 4, big.NewInt(100), []float64{50})
	if err != nil {
		t.Fatal(err)
	}
	if rewards := got["reward"].([][]string); len(rewards) != 4 {
		t.Fatalf("got %d reward rows, want 4", len(rewards))
	}
}

// Near genesis the range starts at block 0; blockCount 0 is an empty result.
func TestFeeHistory_NearGenesisAndZeroCount(t *testing.T) {
	s := &ServiceImpl{}
	got, err := s.FeeHistory(context.Background(), 10, big.NewInt(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["oldestBlock"] != "0x0" || len(got["gasUsedRatio"].([]float64)) != 3 || len(got["baseFeePerGas"].([]string)) != 4 {
		t.Fatalf("near genesis: %v", got)
	}
	got, err = s.FeeHistory(context.Background(), 0, big.NewInt(100), []float64{50})
	if err != nil {
		t.Fatal(err)
	}
	if got["oldestBlock"] != "0x0" || len(got["gasUsedRatio"].([]float64)) != 0 || len(got["baseFeePerGas"].([]string)) != 0 {
		t.Fatalf("blockCount 0: %v", got)
	}
}

// Percentiles multiply the reply size, so their number is capped too, and a
// list geth would refuse (out of range, not ascending) is refused here.
func TestFeeHistory_RejectsBadPercentiles(t *testing.T) {
	s := &ServiceImpl{}
	tooMany := make([]float64, maxFeeHistoryPercentiles+1)
	cases := []struct {
		name string
		perc []float64
		want string
	}{
		{"too many", tooMany, "too many reward percentiles"},
		{"negative", []float64{-1}, "outside 0 to 100"},
		{"above 100", []float64{50, 101}, "outside 0 to 100"},
		{"descending", []float64{90, 10}, "ascending order"},
	}
	for _, c := range cases {
		_, err := s.FeeHistory(context.Background(), 10, big.NewInt(100), c.perc)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}

	// Exactly the limit, ascending with repeats, is accepted.
	ok := make([]float64, maxFeeHistoryPercentiles)
	if _, err := s.FeeHistory(context.Background(), 10, big.NewInt(100), ok); err != nil {
		t.Fatalf("%d equal percentiles: %v", maxFeeHistoryPercentiles, err)
	}
}
