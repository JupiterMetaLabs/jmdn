package Security

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// The block's AccountNonces field is ADVISORY identity metadata: it is carried
// in the block and persisted with it, but it is deliberately OUTSIDE the
// canonical block hash (which covers transaction contents only). Two copies of
// the same block — one with the field (live/gossip path), one without it
// (DataSync backfill; the proto drops it) — must therefore hash identically,
// or mixed archives would produce false hash mismatches and reconcile churn
// across the fleet.
//
// This test pins that invariant against the plausible regression: changing
// RecomputeBlockHashFromContents / CheckBlockHash to derive the hash from the
// whole (marshaled) block instead of the enumerated transaction contents.
// (The sync-monitor fingerprint is protected structurally: hashBlock takes the
// FastSync block type, which has no AccountNonces field at all.)
func TestCanonicalBlockHash_IgnoresAdvisoryAccountNonces(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tx := config.Transaction{
		From:     &from,
		To:       &to,
		Value:    big.NewInt(12345),
		Type:     0,
		Nonce:    7,
		GasLimit: 21000,
		GasPrice: big.NewInt(1_000_000_000),
	}
	block := &config.ZKBlock{
		Transactions: []config.Transaction{tx},
		Status:       "verified",
		BlockNumber:  42,
	}

	// Canonical hash from transaction contents, then self-check passes.
	block.BlockHash = RecomputeBlockHashFromContents(block.Transactions)
	if ok, err := CheckBlockHash(block); !ok || err != nil {
		t.Fatalf("baseline block must pass CheckBlockHash: ok=%v err=%v", ok, err)
	}
	before := RecomputeBlockHashFromContents(block.Transactions)

	// Attach advisory identity metadata — the recomputed hash must not move,
	// and the block must still pass verification unchanged.
	block.AccountNonces = []config.AccountNonce{
		{Address: to, Nonce: 424242},
		{Address: from, Nonce: 991},
	}
	after := RecomputeBlockHashFromContents(block.Transactions)
	if before != after {
		t.Fatalf("canonical hash must ignore AccountNonces: %x != %x", before, after)
	}
	if ok, err := CheckBlockHash(block); !ok || err != nil {
		t.Fatalf("block with AccountNonces must still pass CheckBlockHash: ok=%v err=%v", ok, err)
	}

	// And stripping the field again is equally invisible.
	block.AccountNonces = nil
	if got := RecomputeBlockHashFromContents(block.Transactions); got != before {
		t.Fatalf("hash must be stable across attach/strip of AccountNonces: %x != %x", got, before)
	}
}
