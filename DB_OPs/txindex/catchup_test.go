package txindex

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"gossipnode/config"
)

// chain is a fake ThebeDB: block n holds (n%3)+1 transactions (always ≥ 1,
// like the testnet), each with a unique hash.
type chain struct {
	mu    sync.Mutex
	tip   uint64
	calls [][2]uint64
}

func (c *chain) block(n uint64) *config.ZKBlock {
	k := int(n%3) + 1
	txs := make([]config.Transaction, 0, k)
	for i := 0; i < k; i++ {
		tx := makeTx(addr(fmt.Sprintf("0x%040x", n*10+uint64(i)+1)), addr("0x00000000000000000000000000000000000000aa"), 0)
		tx.Hash[0], tx.Hash[1], tx.Hash[2], tx.Hash[3] = byte(n>>16), byte(n>>8), byte(n), byte(i)
		txs = append(txs, tx)
	}
	return makeBlock(n, txs...)
}

func (c *chain) load(_ *config.PooledConnection, from, to uint64) ([]*config.ZKBlock, error) {
	c.mu.Lock()
	c.calls = append(c.calls, [2]uint64{from, to})
	c.mu.Unlock()
	var out []*config.ZKBlock
	for n := from; n <= to && n <= c.tip; n++ {
		out = append(out, c.block(n))
	}
	return out, nil
}

func (c *chain) txCount(from, to uint64) uint64 {
	var t uint64
	for n := from; n <= to; n++ {
		t += n%3 + 1
	}
	return t
}

func useChain(t *testing.T, c *chain) {
	t.Helper()
	oldLoad, oldTip := loadBlocks, latestBlockFn
	loadBlocks = c.load
	latestBlockFn = func(context.Context) (uint64, error) { return c.tip, nil }
	t.Cleanup(func() { loadBlocks, latestBlockFn = oldLoad, oldTip })
}

func TestBuildRange_IndexesTransactionsInBatches(t *testing.T) {
	idx := newTestDB(t)
	c := &chain{tip: 1200}
	useChain(t, c)
	require.NoError(t, idx.buildRange(context.Background(), 0, 1200))
	n, err := idx.CountTransactions(context.Background())
	require.NoError(t, err)
	require.Equal(t, c.txCount(0, 1200), n, "every transaction of every caught-up block must be indexed")
	require.Equal(t, [][2]uint64{{0, 499}, {500, 999}, {1000, 1200}}, c.calls)
	last, _ := idx.lastIndexedBlock(context.Background())
	require.EqualValues(t, 1200, last)
}

// Reproduces the testnet state: header-only catch-up marked 0..820 indexed
// with no rows, live blocks 821..924 indexed, no catchup_version. EnsureReady
// must re-index from genesis once, then never again.
func TestEnsureReady_RepairsHeaderOnlyIndex(t *testing.T) {
	idx := newTestDB(t)
	c := &chain{tip: 924}
	useChain(t, c)
	var live []*config.ZKBlock
	for n := uint64(821); n <= 924; n++ {
		live = append(live, c.block(n))
	}
	require.NoError(t, idx.indexBlocks(context.Background(), live))
	before, _ := idx.CountTransactions(context.Background())
	require.Equal(t, c.txCount(821, 924), before)

	require.NoError(t, idx.EnsureReady(context.Background()))
	after, err := idx.CountTransactions(context.Background())
	require.NoError(t, err)
	require.Equal(t, c.txCount(0, 924), after, "blocks 0..820 must be re-indexed with their transactions")
	ver, _ := idx.metaUint(context.Background(), catchupVersionKey)
	require.EqualValues(t, catchupVersion, ver)

	c.calls = nil
	require.NoError(t, idx.EnsureReady(context.Background()))
	require.Empty(t, c.calls, "a repaired, current index must not be re-indexed again")
}

func TestEnsureReady_GapCatchupIncludesTransactions(t *testing.T) {
	idx := newTestDB(t)
	c := &chain{tip: 100}
	useChain(t, c)
	require.NoError(t, idx.EnsureReady(context.Background())) // fresh: 0..100
	c.tip = 160
	c.calls = nil
	require.NoError(t, idx.EnsureReady(context.Background())) // gap 101..160
	require.Equal(t, [][2]uint64{{101, 160}}, c.calls)
	n, _ := idx.CountTransactions(context.Background())
	require.Equal(t, c.txCount(0, 160), n)
}

func TestRetryDropped_ReindexesDroppedBlocks(t *testing.T) {
	idx := newTestDB(t)
	c := &chain{tip: 50}
	useChain(t, c)
	globalMu.Lock()
	prev := globalIdx
	globalIdx = idx
	globalMu.Unlock()
	t.Cleanup(func() { globalMu.Lock(); globalIdx = prev; globalMu.Unlock() })

	droppedMu.Lock()
	dropped[7], dropped[42] = struct{}{}, struct{}{}
	droppedMu.Unlock()
	retryDroppedOnce(context.Background())

	n, _ := idx.CountTransactions(context.Background())
	require.Equal(t, c.txCount(7, 7)+c.txCount(42, 42), n)
	droppedMu.Lock()
	require.Empty(t, dropped)
	droppedMu.Unlock()
}
