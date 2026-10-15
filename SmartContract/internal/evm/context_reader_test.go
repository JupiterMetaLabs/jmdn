package evm

import (
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

// With a ChainReader wired, the block context comes from it — no HTTP.
func TestUpdateBlockContext_UsesChainReader(t *testing.T) {
	var heads int32
	cb := common.HexToAddress("0x00000000000000000000000000000000000000c0")
	SetChainReader(&ChainReader{Head: func() (HeadInfo, error) {
		atomic.AddInt32(&heads, 1)
		return HeadInfo{Number: 77, Timestamp: 1_790_000_000, GasLimit: 25_000_000, Coinbase: cb}, nil
	}})
	t.Cleanup(func() { SetChainReader(nil) })
	m := &BlockHashManager{cache: map[uint64]common.Hash{}, apiEndpoint: "http://127.0.0.1:1"} // unreachable: must not be used

	ctx := DefaultBlockContext(30_000_000)
	if err := m.UpdateBlockContext(&ctx); err != nil {
		t.Fatalf("UpdateBlockContext: %v", err)
	}
	if ctx.BlockNumber.Cmp(big.NewInt(77)) != 0 || ctx.Time != 1_790_000_000 || ctx.GasLimit != 25_000_000 || ctx.Coinbase != cb {
		t.Fatalf("context not taken from reader: num=%v time=%d gas=%d cb=%s", ctx.BlockNumber, ctx.Time, ctx.GasLimit, ctx.Coinbase)
	}
	// Second call within headCacheTTL reuses the cached head.
	ctx2 := DefaultBlockContext(30_000_000)
	_ = m.UpdateBlockContext(&ctx2)
	if n := atomic.LoadInt32(&heads); n != 1 {
		t.Fatalf("head read %d times within TTL, want 1", n)
	}
}

// A reader error leaves the defaults in place (caller logs and continues).
func TestUpdateBlockContext_ReaderErrorKeepsDefaults(t *testing.T) {
	SetChainReader(&ChainReader{Head: func() (HeadInfo, error) { return HeadInfo{}, errors.New("no blocks") }})
	t.Cleanup(func() { SetChainReader(nil) })
	m := &BlockHashManager{cache: map[uint64]common.Hash{}}
	ctx := DefaultBlockContext(30_000_000)
	if err := m.UpdateBlockContext(&ctx); err == nil {
		t.Fatal("want the reader error")
	}
	if ctx.GasLimit != 30_000_000 || ctx.BlockNumber.Uint64() != 1 {
		t.Fatal("defaults must be untouched on error")
	}
}

// GetHash resolves through the reader's HashAt.
func TestGetHash_UsesChainReader(t *testing.T) {
	want := common.HexToHash("0xabc")
	SetChainReader(&ChainReader{HashAt: func(n uint64) (common.Hash, error) { return want, nil }})
	t.Cleanup(func() { SetChainReader(nil) })
	m := &BlockHashManager{cache: map[uint64]common.Hash{}, apiEndpoint: "http://127.0.0.1:1"}
	if got := m.GetHash(5); got != want {
		t.Fatalf("GetHash = %s, want %s", got, want)
	}
}

// HTTP fallback: the Block API serves a bare config.ZKBlock. The previous
// decoder expected a {"block":{…}} envelope and decoded zeros; now the real
// fields come through.
func TestUpdateBlockContext_HTTPFallbackDecodesBareBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/latest-block":
			_, _ = w.Write([]byte(`{"blocknumber":12,"timestamp":1790000123,"gaslimit":21000000,"coinbaseaddr":"0x00000000000000000000000000000000000000c1","starkproof":"AAAA"}`))
		case "/api/block/3":
			_, _ = w.Write([]byte(`{"blocknumber":3,"blockhash":"0x0000000000000000000000000000000000000000000000000000000000000def"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	SetChainReader(nil)
	m := &BlockHashManager{cache: map[uint64]common.Hash{}, apiEndpoint: srv.URL}

	var ctx vm.BlockContext = DefaultBlockContext(30_000_000)
	if err := m.UpdateBlockContext(&ctx); err != nil {
		t.Fatalf("UpdateBlockContext: %v", err)
	}
	if ctx.BlockNumber.Uint64() != 12 || ctx.Time != 1790000123 || ctx.GasLimit != 21_000_000 ||
		ctx.Coinbase != common.HexToAddress("0x00000000000000000000000000000000000000c1") {
		t.Fatalf("fallback decode wrong: num=%v time=%d gas=%d cb=%s", ctx.BlockNumber, ctx.Time, ctx.GasLimit, ctx.Coinbase)
	}
	if got := m.GetHash(3); got != common.HexToHash("0xdef") {
		t.Fatalf("fallback GetHash = %s", got)
	}
}

// Empty chain over HTTP ({"message": …}) is an error, not a zero-filled context.
func TestUpdateBlockContext_HTTPEmptyChainIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"no blocks in the chain yet"}`))
	}))
	defer srv.Close()
	SetChainReader(nil)
	m := &BlockHashManager{cache: map[uint64]common.Hash{}, apiEndpoint: srv.URL}
	ctx := DefaultBlockContext(30_000_000)
	if err := m.UpdateBlockContext(&ctx); err == nil {
		t.Fatal("empty chain must be an error so defaults are kept")
	}
	if ctx.GasLimit != 30_000_000 {
		t.Fatal("defaults must be kept")
	}
}
