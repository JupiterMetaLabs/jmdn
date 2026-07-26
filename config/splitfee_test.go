package config

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func bi(n int64) *big.Int { return big.NewInt(n) }

func sumCredits(cs []FeeCredit) *big.Int {
	s := new(big.Int)
	for _, c := range cs {
		s.Add(s, c.Amount)
	}
	return s
}

// Empty recipients must reproduce the historical split EXACTLY:
// zkvm = floor(gasFee/2), coinbase = gasFee - zkvm (half + odd-wei remainder),
// as a single credit to the coinbase address.
func TestSplitFee_EmptyMatchesLegacy(t *testing.T) {
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	for _, gf := range []int64{0, 1, 2, 3, 100, 101, 35_000_000_000, 35_000_000_001} {
		zkvm, credits := SplitFee(bi(gf), cb, nil)
		wantZ := bi(gf / 2)
		wantCB := new(big.Int).Sub(bi(gf), wantZ)
		if zkvm.Cmp(wantZ) != 0 {
			t.Fatalf("gf=%d zkvm=%s want %s", gf, zkvm, wantZ)
		}
		if len(credits) != 1 || credits[0].Addr != cb || credits[0].Amount.Cmp(wantCB) != 0 {
			t.Fatalf("gf=%d credits=%v want single {%s,%s}", gf, credits, cb.Hex(), wantCB)
		}
		if got := new(big.Int).Add(zkvm, sumCredits(credits)); got.Cmp(bi(gf)) != 0 {
			t.Fatalf("gf=%d invariant broken: zkvm+credits=%s != %d", gf, got, gf)
		}
	}
}

// Invariant zkvm + Σcredits == gasFee must hold for weighted splits, including
// awkward gasFee/weight combinations that leave a remainder.
func TestSplitFee_WeightedInvariantAndRemainder(t *testing.T) {
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	a := common.HexToAddress("0x0000000000000000000000000000000000000A11")
	b := common.HexToAddress("0x0000000000000000000000000000000000000B22")
	c := common.HexToAddress("0x0000000000000000000000000000000000000C33")
	recips := []FeeRecipient{{a, 1}, {b, 1}, {c, 1}} // 3 equal weights → remainder likely

	for _, gf := range []int64{0, 1, 5, 7, 100, 101, 999_999_999_999} {
		zkvm, credits := SplitFee(bi(gf), cb, recips)
		total := new(big.Int).Add(zkvm, sumCredits(credits))
		if total.Cmp(bi(gf)) != 0 {
			t.Fatalf("gf=%d invariant broken: zkvm(%s)+credits(%s)=%s != %d", gf, zkvm, sumCredits(credits), total, gf)
		}
		// coinbase share is fully distributed across the recipients (not to cb).
		for _, cr := range credits {
			if cr.Addr == cb {
				t.Fatalf("gf=%d credited the coinbase address despite non-empty recipients", gf)
			}
		}
	}
}

// Distribution must be independent of the input ordering of recipients
// (canonical sort by address), so every node computes identical balances.
func TestSplitFee_OrderIndependent(t *testing.T) {
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	a := common.HexToAddress("0x0000000000000000000000000000000000000A11")
	b := common.HexToAddress("0x0000000000000000000000000000000000000B22")
	gf := bi(101)

	_, c1 := SplitFee(gf, cb, []FeeRecipient{{a, 3}, {b, 1}})
	_, c2 := SplitFee(gf, cb, []FeeRecipient{{b, 1}, {a, 3}})

	got := map[common.Address]string{}
	for _, c := range c1 {
		got[c.Addr] = c.Amount.String()
	}
	for _, c := range c2 {
		if got[c.Addr] != c.Amount.String() {
			t.Fatalf("order-dependent split for %s: %s vs %s", c.Addr.Hex(), got[c.Addr], c.Amount)
		}
	}
}

// All-zero weights must fall back to the single-coinbase credit (no fee lost).
func TestSplitFee_ZeroWeightFallback(t *testing.T) {
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	a := common.HexToAddress("0x0000000000000000000000000000000000000A11")
	zkvm, credits := SplitFee(bi(100), cb, []FeeRecipient{{a, 0}})
	if len(credits) != 1 || credits[0].Addr != cb {
		t.Fatalf("zero-weight should fall back to single coinbase credit, got %v", credits)
	}
	if new(big.Int).Add(zkvm, sumCredits(credits)).Cmp(bi(100)) != 0 {
		t.Fatalf("invariant broken on zero-weight fallback")
	}
}
