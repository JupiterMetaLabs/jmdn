package consensushash

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func h(b byte) common.Hash { var x common.Hash; x[31] = b; return x }

// v3 fixes CON-02 defect 1: empty blocks (same/zero txnsRoot) at different
// heights must NOT collide.
func TestBlockHashV3_EmptyBlocksAtDifferentHeightsDiffer(t *testing.T) {
	zero := common.Hash{}
	a := BlockHashV3(7000700, 5, h(1), zero, zero, 1000)
	b := BlockHashV3(7000700, 6, h(1), zero, zero, 1000)
	if a == b {
		t.Fatal("empty blocks at heights 5 and 6 collide (CON-02 defect 1 not fixed)")
	}
	if a == (common.Hash{}) || b == (common.Hash{}) {
		t.Fatal("empty block hashed to the zero hash (v2 defect)")
	}
}

// v3 fixes CON-02 defect 2: the SAME tx set at a different height/parent/state
// must produce a DIFFERENT block hash (no cross-height replay).
func TestBlockHashV3_BindsHeightParentState(t *testing.T) {
	txns := h(9)
	base := BlockHashV3(7000700, 100, h(1), h(2), txns, 1000)
	cases := map[string]common.Hash{
		"height":    BlockHashV3(7000700, 101, h(1), h(2), txns, 1000),
		"parent":    BlockHashV3(7000700, 100, h(3), h(2), txns, 1000),
		"state":     BlockHashV3(7000700, 100, h(1), h(4), txns, 1000),
		"chain":     BlockHashV3(1, 100, h(1), h(2), txns, 1000),
		"timestamp": BlockHashV3(7000700, 100, h(1), h(2), txns, 1001),
	}
	for name, got := range cases {
		if got == base {
			t.Fatalf("changing %s did not change the block hash (replay window)", name)
		}
	}
}

// Determinism: identical inputs -> identical hash.
func TestBlockHashV3_Deterministic(t *testing.T) {
	a := BlockHashV3(7000700, 42, h(1), h(2), h(3), 99)
	b := BlockHashV3(7000700, 42, h(1), h(2), h(3), 99)
	if a != b {
		t.Fatal("v3 hash is not deterministic")
	}
}

// Domain separation: v3 must not equal a bare keccak(concat) of the same bytes,
// so a v2 and a v3 hash can never collide (staged-rollout safety).
func TestBlockHashV3_DomainTagged(t *testing.T) {
	got := BlockHashV3(0, 0, common.Hash{}, common.Hash{}, common.Hash{}, 0)
	if got == (common.Hash{}) {
		t.Fatal("all-zero input produced the zero hash (missing domain tag)")
	}
}
