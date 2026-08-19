package contractDB

import (
	"testing"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/consensushash"
)

// Host-gated (CGO): needs a real ThebeDB store + go-ethereum crypto.
//   CGO_ENABLED=1 go test ./DB_OPs/contractDB/ -run TestStorageRoot

func newTestStore(t *testing.T) kv.Store {
	t.Helper()
	s, err := kv.NewStore(kv.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putSlot(t *testing.T, s kv.Store, addr common.Address, slot, val common.Hash) {
	t.Helper()
	if err := s.PutDerived(kvKeyStorage(addr, slot), []byte(val.Hex())); err != nil {
		t.Fatal(err)
	}
}

// ComputeStorageRoot is deterministic and independent of write order (ScanPrefix
// sorts), and changes when any slot changes.
func TestStorageRoot_DeterministicAndBinds(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	s1 := newTestStore(t)
	putSlot(t, s1, addr, common.HexToHash("0x01"), common.HexToHash("0x11"))
	putSlot(t, s1, addr, common.HexToHash("0x02"), common.HexToHash("0x22"))
	putSlot(t, s1, addr, common.HexToHash("0x03"), common.HexToHash("0x33"))
	r1, err := ComputeStorageRoot(s1, addr)
	if err != nil {
		t.Fatal(err)
	}

	// Same slots, inserted in a different order → identical root.
	s2 := newTestStore(t)
	putSlot(t, s2, addr, common.HexToHash("0x03"), common.HexToHash("0x33"))
	putSlot(t, s2, addr, common.HexToHash("0x01"), common.HexToHash("0x11"))
	putSlot(t, s2, addr, common.HexToHash("0x02"), common.HexToHash("0x22"))
	r2, err := ComputeStorageRoot(s2, addr)
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Fatalf("storage root not order-independent: %s != %s", r1.Hex(), r2.Hex())
	}

	// Change one slot value → root changes.
	putSlot(t, s2, addr, common.HexToHash("0x02"), common.HexToHash("0x99"))
	r3, err := ComputeStorageRoot(s2, addr)
	if err != nil {
		t.Fatal(err)
	}
	if r3 == r1 {
		t.Fatal("changing a slot value did not change the storage root")
	}

	// Empty contract → domain-only digest, non-zero, and != a populated root.
	empty, err := ComputeStorageRoot(newTestStore(t), addr)
	if err != nil {
		t.Fatal(err)
	}
	if empty == (common.Hash{}) {
		t.Fatal("empty storage root should be the domain digest, not zero")
	}
	if empty == r1 {
		t.Fatal("empty and populated storage roots must differ")
	}
}

// A tombstoned (empty-value) slot is treated as absent.
func TestStorageRoot_SkipsTombstone(t *testing.T) {
	addr := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	s := newTestStore(t)
	putSlot(t, s, addr, common.HexToHash("0x01"), common.HexToHash("0x11"))
	withTomb, _ := ComputeStorageRoot(s, addr)

	// Add a tombstone (empty value) for another slot → must not change the root.
	if err := s.PutDerived(kvKeyStorage(addr, common.HexToHash("0x02")), []byte{}); err != nil {
		t.Fatal(err)
	}
	after, _ := ComputeStorageRoot(s, addr)
	if after != withTomb {
		t.Fatal("tombstoned slot must not affect the storage root")
	}
}

// FoldAllContracts folds each live contract; changing one contract's storage
// changes the overall fingerprint, and a tombstoned code entry is skipped.
func TestFoldAllContracts_Binds(t *testing.T) {
	s := newTestStore(t)
	a := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	b := common.HexToAddress("0x00000000000000000000000000000000000000b2")
	if err := s.PutDerived(kvKeyCode(a), []byte{0x60, 0x01}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDerived(kvKeyCode(b), []byte{0x60, 0x02}); err != nil {
		t.Fatal(err)
	}
	putSlot(t, s, a, common.HexToHash("0x01"), common.HexToHash("0x11"))

	fold := func() common.Hash {
		f := consensushash.NewStateFingerprinterV1()
		if err := FoldAllContracts(s, f); err != nil {
			t.Fatal(err)
		}
		return f.Sum()
	}
	base := fold()

	// Change contract a's storage → fingerprint changes.
	putSlot(t, s, a, common.HexToHash("0x01"), common.HexToHash("0xff"))
	if fold() == base {
		t.Fatal("changing a contract's storage did not change the fold")
	}

	// Tombstone contract b's code → b is skipped (fingerprint changes again).
	prev := fold()
	if err := s.PutDerived(kvKeyCode(b), []byte{}); err != nil {
		t.Fatal(err)
	}
	if fold() == prev {
		t.Fatal("removing a contract (tombstoned code) did not change the fold")
	}
}
