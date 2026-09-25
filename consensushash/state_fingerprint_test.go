package consensushash

import (
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func acct(addr byte, bal string, nonce, sent uint64) AccountLeaf {
	return AccountLeaf{Address: "0x" + string(rune('a'+addr)), Balance: bal, TxNonce: nonce, TxCountSent: sent}
}

func ctr(addr byte, nonce uint64, code, root byte) ContractLeaf {
	return ContractLeaf{Address: "0x" + string(rune('a'+addr)), Nonce: nonce, CodeHash: h(code), StorageRoot: h(root)}
}

// The domain tag guarantees an empty state is not the zero hash (so a header that
// forgot to set the fingerprint is distinguishable from a genuinely-empty state).
func TestStateFingerprintV1_EmptyIsDomainTagged(t *testing.T) {
	got := StateFingerprintV1(nil, nil)
	if got == (common.Hash{}) {
		t.Fatal("empty state produced the zero hash (missing domain tag)")
	}
}

// Order independence: the same set of leaves in any order yields the same hash.
func TestStateFingerprintV1_OrderIndependent(t *testing.T) {
	accounts := []AccountLeaf{
		acct(3, "100", 1, 0), acct(1, "5", 9, 2), acct(2, "", 0, 0), acct(0, "77", 4, 4),
	}
	contracts := []ContractLeaf{
		ctr(2, 3, 9, 9), ctr(0, 1, 1, 2), ctr(1, 0, 5, 6),
	}
	want := StateFingerprintV1(accounts, contracts)

	for i := 0; i < 50; i++ {
		a := append([]AccountLeaf(nil), accounts...)
		c := append([]ContractLeaf(nil), contracts...)
		rand.Shuffle(len(a), func(x, y int) { a[x], a[y] = a[y], a[x] })
		rand.Shuffle(len(c), func(x, y int) { c[x], c[y] = c[y], c[x] })
		if got := StateFingerprintV1(a, c); got != want {
			t.Fatalf("shuffle %d changed the fingerprint (not order-independent)", i)
		}
	}
}

// Each consensus-relevant field must bind: changing it must change the hash.
func TestStateFingerprintV1_FieldsBind(t *testing.T) {
	baseAcc := []AccountLeaf{acct(0, "100", 1, 1)}
	baseCon := []ContractLeaf{ctr(0, 1, 1, 1)}
	base := StateFingerprintV1(baseAcc, baseCon)

	cases := map[string]common.Hash{
		"balance":     StateFingerprintV1([]AccountLeaf{acct(0, "101", 1, 1)}, baseCon),
		"txNonce":     StateFingerprintV1([]AccountLeaf{acct(0, "100", 2, 1)}, baseCon),
		"txCountSent": StateFingerprintV1([]AccountLeaf{acct(0, "100", 1, 2)}, baseCon),
		"acctAddr":    StateFingerprintV1([]AccountLeaf{acct(5, "100", 1, 1)}, baseCon),
		"ctrNonce":    StateFingerprintV1(baseAcc, []ContractLeaf{ctr(0, 2, 1, 1)}),
		"ctrCode":     StateFingerprintV1(baseAcc, []ContractLeaf{ctr(0, 1, 9, 1)}),
		"ctrRoot":     StateFingerprintV1(baseAcc, []ContractLeaf{ctr(0, 1, 1, 9)}),
		"ctrAddr":     StateFingerprintV1(baseAcc, []ContractLeaf{ctr(5, 1, 1, 1)}),
	}
	for name, got := range cases {
		if got == base {
			t.Fatalf("changing %s did not change the fingerprint", name)
		}
	}
}

// Balance "" and "0" must be the SAME (both mean zero balance), or two nodes that
// write a zero balance differently would falsely diverge.
func TestStateFingerprintV1_EmptyBalanceEqualsZero(t *testing.T) {
	a := StateFingerprintV1([]AccountLeaf{acct(0, "", 0, 0)}, nil)
	b := StateFingerprintV1([]AccountLeaf{acct(0, "0", 0, 0)}, nil)
	if a != b {
		t.Fatal(`balance "" and "0" produced different fingerprints`)
	}
}

// Address casing must not matter (write paths differ on checksum casing).
func TestStateFingerprintV1_AddressCaseInsensitive(t *testing.T) {
	lower := StateFingerprintV1([]AccountLeaf{{Address: "0xabcdef", Balance: "1"}}, nil)
	upper := StateFingerprintV1([]AccountLeaf{{Address: "0xABCDEF", Balance: "1"}}, nil)
	if lower != upper {
		t.Fatal("fingerprint depends on address checksum casing")
	}
}

// An account section and a contract section must not cross-cancel: a single
// account leaf must not hash the same as a single contract leaf, even with the
// same address, because of the record type tag + distinct field layout.
func TestStateFingerprintV1_SectionsSeparated(t *testing.T) {
	onlyAcct := StateFingerprintV1([]AccountLeaf{acct(0, "0", 0, 0)}, nil)
	onlyCtr := StateFingerprintV1(nil, []ContractLeaf{ctr(0, 0, 0, 0)})
	if onlyAcct == onlyCtr {
		t.Fatal("an account leaf and a contract leaf collide (missing section tag)")
	}
}

// Length-prefix framing must be injective: splitting a field across the boundary
// must not alias. ("ab","c") vs ("a","bc") as (address,balance) must differ.
func TestStateFingerprintV1_FramingUnambiguous(t *testing.T) {
	x := StateFingerprintV1([]AccountLeaf{{Address: "ab", Balance: "c"}}, nil)
	y := StateFingerprintV1([]AccountLeaf{{Address: "a", Balance: "bc"}}, nil)
	if x == y {
		t.Fatal("field boundary is ambiguous (missing length prefix)")
	}
}

// Determinism: identical inputs -> identical hash.
func TestStateFingerprintV1_Deterministic(t *testing.T) {
	a := StateFingerprintV1([]AccountLeaf{acct(0, "100", 1, 1)}, []ContractLeaf{ctr(0, 1, 1, 1)})
	b := StateFingerprintV1([]AccountLeaf{acct(0, "100", 1, 1)}, []ContractLeaf{ctr(0, 1, 1, 1)})
	if a != b {
		t.Fatal("fingerprint is not deterministic")
	}
}

// The streaming folder (runtime path) must produce the SAME hash as the batch
// function when fed the same set in canonical order.
func TestStateFingerprintV1_StreamingMatchesBatch(t *testing.T) {
	accounts := []AccountLeaf{acct(0, "77", 4, 4), acct(1, "5", 9, 2), acct(2, "", 0, 0)}
	contracts := []ContractLeaf{ctr(0, 1, 1, 2), ctr(1, 0, 5, 6)}
	batch := StateFingerprintV1(accounts, contracts)

	// Feed in canonical order (accounts already ascending by address, then contracts).
	f := NewStateFingerprinterV1()
	for _, a := range accounts {
		f.FoldAccount(a)
	}
	for _, c := range contracts {
		f.FoldContract(c)
	}
	if f.Sum() != batch {
		t.Fatal("streaming folder disagrees with batch StateFingerprintV1")
	}
}
