package Sequencer

import (
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// Whether a stored block's transactions were applied is read from their
// per-transaction markers.
func TestClassifyStoredBlock(t *testing.T) {
	a, b := "0xa", "0xb"
	cases := []struct {
		name    string
		hashes  []string
		applied map[string]bool
		want    storedState
	}{
		{"no transactions counts as applied", nil, nil, storedSameApplied},
		{"all applied, block marker missing", []string{a, b}, map[string]bool{a: true, b: true}, storedSameApplied},
		{"none applied: rolled back, write landed later", []string{a, b}, map[string]bool{}, storedSameUnapplied},
		{"some applied", []string{a, b}, map[string]bool{a: true}, storedSamePartial},
		{"marker for another transaction does not count", []string{a}, map[string]bool{b: true}, storedSameUnapplied},
	}
	for _, c := range cases {
		if got := classifyStoredBlock(c.hashes, c.applied); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A transaction listed twice in a block is applied once, so it is counted once.
func TestBlockTxHashesDeduplicates(t *testing.T) {
	h1, h2 := common.HexToHash("0x01"), common.HexToHash("0x02")
	block := &config.ZKBlock{Transactions: []config.Transaction{{Hash: h1}, {Hash: h2}, {Hash: h1}}}
	got := blockTxHashes(block)
	if len(got) != 2 || got[0] != h1.String() || got[1] != h2.String() {
		t.Fatalf("got %v, want [%s %s]", got, h1.String(), h2.String())
	}
	if classifyStoredBlock(got, map[string]bool{h1.String(): true, h2.String(): true}) != storedSameApplied {
		t.Fatal("a block whose distinct transactions are all applied must count as applied")
	}
}

// loadStoredBlockState end to end, with the store reads replaced.
func TestLoadStoredBlockState(t *testing.T) {
	savedRead, savedBlock, savedTx := readStoredBlock, blockMarkerApplied, txMarkersApplied
	t.Cleanup(func() { readStoredBlock, blockMarkerApplied, txMarkersApplied = savedRead, savedBlock, savedTx })

	tx1, tx2 := common.HexToHash("0x11"), common.HexToHash("0x12")
	withTxs := &config.ZKBlock{BlockNumber: 80, BlockHash: common.HexToHash("0x80"),
		Transactions: []config.Transaction{{Hash: tx1}, {Hash: tx2}}}
	empty := &config.ZKBlock{BlockNumber: 81, BlockHash: common.HexToHash("0x81")}
	other := &config.ZKBlock{BlockNumber: 80, BlockHash: common.HexToHash("0x99")}

	cases := []struct {
		name        string
		block       *config.ZKBlock
		stored      *config.ZKBlock
		blockMarker bool
		txApplied   map[string]bool
		want        storedState
	}{
		{"nothing stored", withTxs, nil, false, nil, storedNone},
		{"different block stored", withTxs, other, false, nil, storedDifferent},
		{"block marker present", withTxs, withTxs, true, nil, storedSameApplied},
		{"no block marker, all tx markers", withTxs, withTxs, false,
			map[string]bool{tx1.String(): true, tx2.String(): true}, storedSameApplied},
		{"no block marker, no tx markers (write landed after rollback)", withTxs, withTxs, false,
			map[string]bool{}, storedSameUnapplied},
		{"no block marker, some tx markers", withTxs, withTxs, false,
			map[string]bool{tx1.String(): true}, storedSamePartial},
		{"block without transactions, no block marker", empty, empty, false, nil, storedSameApplied},
	}
	for _, c := range cases {
		readStoredBlock = func(uint64) (*config.ZKBlock, error) { return c.stored, nil }
		blockMarkerApplied = func(string) (bool, error) { return c.blockMarker, nil }
		txMarkersApplied = func([]string) (map[string]bool, error) { return c.txApplied, nil }
		got, err := loadStoredBlockState(c.block)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v (err %v), want %v", c.name, got, err, c.want)
		}
	}
}
