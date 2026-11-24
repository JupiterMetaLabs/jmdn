package PubSubMessages

// cacheConsensuMessage had no cap and no eviction, so a peer-chosen block hash
// could grow it without limit. These tests cover the cap, the eviction
// direction, and pruning stale entries ahead of a new insert.

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func blockHashForHeight(n uint64) common.Hash {
	return common.BigToHash(new(big.Int).SetUint64(n))
}

func resetConsensusMessageCache() {
	cacheMu.Lock()
	cacheConsensuMessage = make(map[string]*ConsensusMessage)
	cacheMu.Unlock()
}

func cacheHeight(h uint64) {
	msg := &ConsensusMessage{ZKBlock: &config.ZKBlock{BlockNumber: h, BlockHash: blockHashForHeight(h)}}
	msg.SetGloalVarCacheConsensusMessage()
}

// TestSetGloalVarCacheConsensusMessage_HonestLowEntrySurvivesAHigherFlood is
// the realistic threat case: the honest, in-flight proposal is cached first,
// at the lowest height present (closest to the local tip). A later flood of
// higher-height junk — past the cap — must evict itself down, never the
// honest entry, because eviction picks the HIGHEST height present, not the
// lowest.
func TestSetGloalVarCacheConsensusMessage_HonestLowEntrySurvivesAHigherFlood(t *testing.T) {
	resetConsensusMessageCache()

	const honestHeight = 100
	cacheHeight(honestHeight)

	// Flood with more than the cap's worth of higher heights.
	for h := uint64(200); h < 200+MaxCachedConsensusMessages+3; h++ {
		cacheHeight(h)
	}

	snap := SnapshotConsensusMessages()
	if len(snap) != MaxCachedConsensusMessages {
		t.Fatalf("cache size = %d, want %d", len(snap), MaxCachedConsensusMessages)
	}
	if got := LookupConsensusMessageByBlockHash(blockHashForHeight(honestHeight).Hex()); got == nil {
		t.Fatal("the honest low-height entry was evicted by a higher-height flood")
	}
}

// TestSetGloalVarCacheConsensusMessage_CapIsEnforced proves the cache never
// grows past MaxCachedConsensusMessages regardless of how many distinct
// hashes are inserted.
func TestSetGloalVarCacheConsensusMessage_CapIsEnforced(t *testing.T) {
	resetConsensusMessageCache()

	for h := uint64(1); h <= MaxCachedConsensusMessages+10; h++ {
		cacheHeight(h)
	}

	if got := len(SnapshotConsensusMessages()); got != MaxCachedConsensusMessages {
		t.Fatalf("cache size = %d, want %d", got, MaxCachedConsensusMessages)
	}
}

func TestSetGloalVarCacheConsensusMessage_NilZKBlockNeverCached(t *testing.T) {
	resetConsensusMessageCache()

	(&ConsensusMessage{}).SetGloalVarCacheConsensusMessage()

	if len(SnapshotConsensusMessages()) != 0 {
		t.Fatal("a nil-ZKBlock message was cached")
	}
}

func TestPruneConsensusMessagesBelow_RemovesOnlyStaleEntries(t *testing.T) {
	resetConsensusMessageCache()

	for _, h := range []uint64{10, 11, 12, 13} {
		cacheHeight(h)
	}

	PruneConsensusMessagesBelow(12)

	for _, removed := range []uint64{10, 11} {
		if got := LookupConsensusMessageByBlockHash(blockHashForHeight(removed).Hex()); got != nil {
			t.Fatalf("height %d should have been pruned, still present", removed)
		}
	}
	for _, kept := range []uint64{12, 13} {
		if got := LookupConsensusMessageByBlockHash(blockHashForHeight(kept).Hex()); got == nil {
			t.Fatalf("height %d should not have been pruned", kept)
		}
	}
}
