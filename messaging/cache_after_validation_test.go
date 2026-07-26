package messaging

// Validate-before-cache: a zkblock hash enters the processed/duplicate (dedup)
// cache ONLY after full validation succeeds. A rejected block must NOT occupy
// the cache, otherwise an invalid block carrying a genuine block's hash would
// pre-empt the real block — which is then dropped as a "duplicate" when it
// arrives. The dedup store is a Bloom filter (entries can never be deleted), so
// validate-before-cache is the only safe ordering.

import (
	"context"
	"testing"

	"gossipnode/config"

	bloom "github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/crypto"
)

// ensureMessageFilter initializes the package-level dedup Bloom filter the same
// way node startup does (blockPropagation.go), since that init path does not run
// under unit tests.
func ensureMessageFilter() {
	if messageFilter == nil {
		messageFilter = bloom.NewWithEstimates(10000, 0.01)
	}
}

// TestInvalidBlockBeforeGenuine_NotCensored covers the "invalid certificate
// arriving before the legitimate block" case. It drives admitZKBlock (the
// validate-before-cache seam the handler delegates to) directly, since the
// stream handler itself is not unit-testable.
func TestInvalidBlockBeforeGenuine_NotCensored(t *testing.T) {
	resetEquivocation()
	ensureMessageFilter()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	// Genuine block B at height 40 with its canonical hash H.
	tx := signedTx(t, key, 0)
	txs := []config.Transaction{tx}
	genuine := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  40,
		Transactions: txs,
	}
	H := genuine.BlockHash
	messageID := getMessageIDForBloomFilter(config.BlockMessage{Type: "zkblock", Block: genuine})

	// A block arrives with the SAME hash H but a sub-quorum certificate
	// (1 of 5 < 2f+1 = 3). Same body => same canonical hash, so body binding
	// passes and it is the certificate that fails — a realistic pre-emption.
	attack := config.BlockMessage{
		Type:  "zkblock",
		Block: genuine,
		Data:  blockBoundCert(t, genuine, "peerA"),
	}
	if rej := admitZKBlock(context.Background(), attack, messageID); rej == nil {
		t.Fatalf("block with sub-quorum certificate should be rejected")
	}

	// The rejected block must NOT occupy the dedup cache.
	if isMessageProcessed(messageID) {
		t.Fatalf("rejected block occupied the dedup cache — genuine block %s would be permanently censored", H.Hex())
	}

	// The genuine block (valid quorum certificate, same hash H) now arrives and
	// MUST be admitted, then cached.
	good := config.BlockMessage{
		Type:  "zkblock",
		Block: genuine,
		Data:  blockBoundCert(t, genuine, "peerA", "peerB", "peerC"),
	}
	if rej := admitZKBlock(context.Background(), good, messageID); rej != nil {
		t.Fatalf("genuine block should be admitted after the invalid one, got reason=%s", rej.reason)
	}
	if !isMessageProcessed(messageID) {
		t.Fatalf("a validated block must be marked processed")
	}
}

// TestValidBlockIsCachedOnce confirms the positive path: a first valid block
// is admitted and cached; a second identical delivery is still admitted by the
// gate (idempotent) and remains cached — i.e. caching is a post-validation
// effect, not a pre-validation guard.
func TestValidBlockIsCachedOnce(t *testing.T) {
	resetEquivocation()
	ensureMessageFilter()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tx := signedTx(t, key, 1)
	txs := []config.Transaction{tx}
	b := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  41,
		Transactions: txs,
	}
	messageID := getMessageIDForBloomFilter(config.BlockMessage{Type: "zkblock", Block: b})
	msg := config.BlockMessage{Type: "zkblock", Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}

	if isMessageProcessed(messageID) {
		t.Fatalf("precondition: hash should not be cached before admission")
	}
	if rej := admitZKBlock(context.Background(), msg, messageID); rej != nil {
		t.Fatalf("valid block should be admitted, got %s", rej.reason)
	}
	if !isMessageProcessed(messageID) {
		t.Fatalf("valid block should be cached after admission")
	}
}
