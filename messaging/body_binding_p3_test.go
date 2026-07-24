package messaging

// Independent cross-checks that the receiver's canonical recompute matches
// the block generator (JMDT-Sequencer-Orchestrator internal/block/generator.go).
// These reproduce the generator's algorithm inline (a second, independent path)
// so a drift in RecomputeBlockHashFromTxs / RecomputeTxnsRoot is caught.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func txWithHash(h common.Hash) config.Transaction { return config.Transaction{Hash: h} }

// generatorBlockHash reproduces generateBlockHashFromTransactions:
// Keccak256(concat of each tx's 32-byte hash).
func generatorBlockHash(txs []config.Transaction) common.Hash {
	var buf []byte
	for i := range txs {
		buf = append(buf, txs[i].Hash.Bytes()...)
	}
	return common.BytesToHash(crypto.Keccak256(buf))
}

// generatorTxnsRoot reproduces generateMerkleRoot (SHA256 binary merkle).
func generatorTxnsRoot(txs []config.Transaction) string {
	level := make([][]byte, len(txs))
	for i := range txs {
		level[i] = txs[i].Hash.Bytes()
	}
	if len(level) == 1 {
		c := append(append([]byte{}, level[0]...), level[0]...)
		s := sha256.Sum256(c)
		return "0x" + hex.EncodeToString(s[:])
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			c := append(append([]byte{}, level[i]...), level[i+1]...)
			s := sha256.Sum256(c)
			next = append(next, s[:])
		}
		level = next
	}
	return "0x" + hex.EncodeToString(level[0])
}

func TestP3_RecomputeMatchesGenerator(t *testing.T) {
	mk := func(b byte) common.Hash {
		var h common.Hash
		for i := range h {
			h[i] = b
		}
		return h
	}
	for _, n := range []int{1, 2, 3, 4, 5, 7, 10, 13} {
		var txs []config.Transaction
		for i := 0; i < n; i++ {
			txs = append(txs, txWithHash(mk(byte(i+1))))
		}
		if got, want := RecomputeBlockHashFromTxs(txs), generatorBlockHash(txs); got != want {
			t.Errorf("n=%d: RecomputeBlockHashFromTxs=%s, generator=%s", n, got.Hex(), want.Hex())
		}
		got := strings.ToLower(RecomputeTxnsRoot(txs))
		want := strings.ToLower(generatorTxnsRoot(txs))
		if got != want {
			t.Errorf("n=%d: RecomputeTxnsRoot=%s, generator=%s", n, got, want)
		}
	}
}

// stateRootChain must match generateStateRoot: Keccak256(parentStateRoot||blockHash).
func TestP3_StateRootChainMatchesGenerator(t *testing.T) {
	parent := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	blockHash := common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321")

	got, ok := stateRootChain(parent, blockHash)
	if !ok {
		t.Fatal("expected ok for non-zero parent")
	}
	want := common.BytesToHash(crypto.Keccak256(append(parent.Bytes(), blockHash.Bytes()...)))
	if got != want {
		t.Fatalf("stateRootChain=%s, want %s", got.Hex(), want.Hex())
	}

	// Zero parent state root => not enforceable (fresh/legacy parent).
	if _, ok := stateRootChain(common.Hash{}, blockHash); ok {
		t.Fatal("zero parent state root must not be enforceable")
	}
}
