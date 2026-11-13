package logstore

// S4 (review) regression tests for eth_getLogs DoS bounds. They assert three
// properties independent of any live DB:
//   - an address-less query with no bounded range is rejected (ErrQueryUnbounded),
//     BEFORE any scan runs;
//   - a bounded address-less query seeks per block and never scans from block 0
//     (the old ScanPrefix(primaryPrefix) walk);
//   - any query that would return more than maxLogResults fails with
//     ErrTooManyResults instead of buffering an unbounded slice.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs/store"
)

// recordKV is a sorted in-memory KV (like the memKV in logstore_test.go) that also
// records every prefix passed to ScanPrefix, so tests can assert the access
// pattern — specifically that the address-less path no longer does the from-zero
// ScanPrefix(primaryPrefix) walk.
type recordKV struct {
	m             map[string][]byte
	scannedPrefix []string
}

func newRecordKV() *recordKV { return &recordKV{m: map[string][]byte{}} }

func (k *recordKV) Get(key []byte) ([]byte, error) { return k.m[string(key)], nil }

func (k *recordKV) PutDerived(key, value []byte) error {
	k.m[string(key)] = append([]byte(nil), value...)
	return nil
}

func (k *recordKV) ScanPrefix(prefix []byte, fn func(kk, vv []byte) error) error {
	k.scannedPrefix = append(k.scannedPrefix, string(prefix))
	keys := make([]string, 0, len(k.m))
	for kk := range k.m {
		if strings.HasPrefix(kk, string(prefix)) {
			keys = append(keys, kk)
		}
	}
	sort.Strings(keys) // zero-padded keys → lexical order == numeric order
	for _, kk := range keys {
		if err := fn([]byte(kk), k.m[kk]); err != nil {
			return err
		}
	}
	return nil
}

func seedLog(t *testing.T, s *Store, block uint64, txIndex, logIndex uint, addr common.Address) {
	t.Helper()
	l := &ethtypes.Log{Address: addr, Topics: []common.Hash{}, Data: []byte{}, BlockNumber: block, TxIndex: txIndex, Index: logIndex}
	if err := s.StoreLogs(context.Background(), []*ethtypes.Log{l}); err != nil {
		t.Fatalf("seed block %d: %v", block, err)
	}
}

func TestGetLogs_AddresslessUnboundedRejected(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)
	seedLog(t, s, 5, 0, 0, common.HexToAddress("0x01"))

	// ToBlock == 0 → "no upper bound" → must be rejected without scanning.
	_, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 0, ToBlock: 0})
	if !errors.Is(err, ErrQueryUnbounded) {
		t.Fatalf("want ErrQueryUnbounded, got %v", err)
	}
	if len(kv.scannedPrefix) != 0 {
		t.Fatalf("unbounded query must not scan; scanned %v", kv.scannedPrefix)
	}
}

func TestGetLogs_AddresslessSpanTooLarge(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)
	_, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 0, ToBlock: maxLogBlockSpan})
	if !errors.Is(err, ErrQueryUnbounded) {
		t.Fatalf("span %d must be rejected, got %v", maxLogBlockSpan+1, err)
	}
	if len(kv.scannedPrefix) != 0 {
		t.Fatalf("oversized query must not scan; scanned %v", kv.scannedPrefix)
	}
}

// The span bound used to be written as ToBlock-FromBlock+1 > maxLogBlockSpan.
// In uint64 arithmetic that expression wraps to 0 for ToBlock = MaxUint64, so
// the widest request expressible — {fromBlock:"0x0", toBlock:"0xffffffffffffffff"},
// which any unauthenticated caller can send to the public RPC port — passed the
// bound and started a ~1.8e19-iteration scan that no deadline could stop.
func TestGetLogs_AddresslessSpanOverflowRejected(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)

	_, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 0, ToBlock: math.MaxUint64})
	if !errors.Is(err, ErrQueryUnbounded) {
		t.Fatalf("toBlock=MaxUint64 must be rejected, got %v", err)
	}
	if len(kv.scannedPrefix) != 0 {
		t.Fatalf("overflow query must not scan; scanned %v", kv.scannedPrefix)
	}
}

// The bound itself is unchanged: maxLogBlockSpan blocks inclusive is the widest
// accepted range, one more is refused. Pins that dropping the +1 did not move
// the limit by one.
func TestGetLogs_AddresslessSpanBoundary(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)

	// maxLogBlockSpan blocks inclusive: [1, maxLogBlockSpan] — allowed.
	if _, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 1, ToBlock: maxLogBlockSpan}); err != nil {
		t.Fatalf("a %d-block span must be accepted, got %v", maxLogBlockSpan, err)
	}
	// One block more — refused.
	if _, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 1, ToBlock: maxLogBlockSpan + 1}); !errors.Is(err, ErrQueryUnbounded) {
		t.Fatalf("a %d-block span must be rejected, got %v", maxLogBlockSpan+1, err)
	}
}

// GetLogs took a context and discarded it (the parameter was named `_`), so the
// 10s deadline DB_OPs.GetLogs creates for this call could never be observed.
// Both scan paths must now stop on a cancelled context.
func TestGetLogs_HonoursContextCancellation(t *testing.T) {
	addr := common.HexToAddress("0x03")

	t.Run("address-less path", func(t *testing.T) {
		kv := newRecordKV()
		s := New(kv)
		seedLog(t, s, 1, 0, 0, addr)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.GetLogs(ctx, store.LogFilter{FromBlock: 1, ToBlock: 10}); !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	})

	t.Run("address path", func(t *testing.T) {
		kv := newRecordKV()
		s := New(kv)
		seedLog(t, s, 1, 0, 0, addr)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.GetLogs(ctx, store.LogFilter{Addresses: []common.Address{addr}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	})
}

// SetLimits is the startup injection point for rpc.logs.*. A zero or negative
// value must keep the compiled default rather than meaning "unlimited" — a
// malformed config must not open the endpoint.
func TestSetLimits_FailsClosedOnZero(t *testing.T) {
	t.Cleanup(func() { SetLimits(maxLogBlockSpan, maxLogResults, maxLogScanBlocks) })

	SetLimits(0, 0, 0)
	span, results, scan := Limits()
	if span != maxLogBlockSpan || results != maxLogResults || scan != maxLogScanBlocks {
		t.Fatalf("zero config must keep the defaults, got span=%d results=%d scan=%d", span, results, scan)
	}

	SetLimits(500, 250, 400)
	span, results, scan = Limits()
	if span != 500 || results != 250 || scan != 400 {
		t.Fatalf("configured values not applied: span=%d results=%d scan=%d", span, results, scan)
	}

	// A partial config overrides only what it sets.
	SetLimits(0, 99, 0)
	span, results, scan = Limits()
	if span != 500 || results != 99 || scan != 400 {
		t.Fatalf("partial config must leave the others alone: span=%d results=%d scan=%d", span, results, scan)
	}
}

// The address path walks a contract's whole history from block 0, and the
// result cap is only reached once a log MATCHES — so a filter matching nothing
// scanned everything. The scan budget is the only bound on that work.
func TestGetLogs_AddressPathHonoursScanBudget(t *testing.T) {
	t.Cleanup(func() { SetLimits(maxLogBlockSpan, maxLogResults, maxLogScanBlocks) })

	kv := newRecordKV()
	s := New(kv)
	addr := common.HexToAddress("0x04")
	for b := uint64(1); b <= 20; b++ {
		seedLog(t, s, b, 0, 0, addr)
	}

	// Budget of 5 blocks, a filter that matches nothing (so the result cap can
	// never fire) and no block range at all — the shape the address path could
	// not refuse before.
	SetLimits(0, 0, 5)
	noSuchTopic := [][]common.Hash{{common.HexToHash("0xdead")}}
	_, err := s.GetLogs(context.Background(), store.LogFilter{
		Addresses: []common.Address{addr},
		Topics:    noSuchTopic,
	})
	if !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("want ErrScanBudgetExceeded once the budget is spent, got %v", err)
	}

	// Within budget the same query succeeds and returns nothing.
	SetLimits(0, 0, 100)
	got, err := s.GetLogs(context.Background(), store.LogFilter{
		Addresses: []common.Address{addr},
		Topics:    noSuchTopic,
	})
	if err != nil {
		t.Fatalf("a query inside the budget must succeed, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("filter matches nothing, want 0 logs, got %d", len(got))
	}
}

// The configured span is what the address-less bound uses, not the compiled
// constant.
// REGRESSION (review of #291): the address index starts at block 0, so the scan
// passes every block the contract has ever had a log in before it reaches the
// requested range. Counting those against the budget made the budget a function
// of the contract's HISTORY rather than of the work asked for: once a contract
// had logs in more than max_scan_blocks blocks, EVERY query on it was refused,
// including one for the last ten blocks.
//
// That is not a theoretical shape — it is how the JMDT-DEX relayer reads vault
// deposits (eth_getLogs(address=vault)), so on a busy mainnet vault bridging
// would have stopped outright.
//
// Only blocks inside [FromBlock, ToBlock] count. The walk up to FromBlock is
// still bounded, by the caller's deadline (#288).
func TestGetLogs_AddressPathBudgetCountsOnlyTheRequestedRange(t *testing.T) {
	t.Cleanup(func() { SetLimits(maxLogBlockSpan, maxLogResults, maxLogScanBlocks) })

	kv := newRecordKV()
	s := New(kv)
	addr := common.HexToAddress("0x05")
	const history = 30
	for b := uint64(1); b <= history; b++ {
		seedLog(t, s, b, 0, 0, addr)
	}

	// Budget far smaller than the contract's history, as on a busy mainnet
	// contract. The requested window is well inside it.
	SetLimits(0, 0, 5)
	got, err := s.GetLogs(context.Background(), store.LogFilter{
		Addresses: []common.Address{addr},
		FromBlock: history - 4, // 26
		ToBlock:   history,     // 30 -> 5 blocks, exactly the budget
	})
	if err != nil {
		t.Fatalf("a 5-block window on a %d-block history must be served, got %v", history, err)
	}
	if len(got) != 5 {
		t.Fatalf("want the 5 in-range logs, got %d", len(got))
	}

	// The budget still bites on the work actually requested.
	if _, err := s.GetLogs(context.Background(), store.LogFilter{
		Addresses: []common.Address{addr},
		FromBlock: 1,
		ToBlock:   history,
	}); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("a %d-block window under a 5-block budget must be refused, got %v", history, err)
	}
}

func TestGetLogs_AddresslessUsesConfiguredSpan(t *testing.T) {
	t.Cleanup(func() { SetLimits(maxLogBlockSpan, maxLogResults, maxLogScanBlocks) })

	kv := newRecordKV()
	s := New(kv)
	SetLimits(10, 0, 0) // 10 blocks inclusive is the widest accepted range

	if _, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 1, ToBlock: 10}); err != nil {
		t.Fatalf("a 10-block span must be accepted under span=10, got %v", err)
	}
	if _, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 1, ToBlock: 11}); !errors.Is(err, ErrQueryUnbounded) {
		t.Fatalf("an 11-block span must be rejected under span=10, got %v", err)
	}
}

func TestGetLogs_AddresslessBoundedSeeksPerBlock(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)
	addr := common.HexToAddress("0x02")
	// Logs at blocks 1, 50, 100. Query [50,52] must return only block 50 and must
	// NOT issue the from-zero primaryPrefix scan.
	for _, b := range []uint64{1, 50, 100} {
		seedLog(t, s, b, 0, 0, addr)
	}
	got, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 50, ToBlock: 52})
	if err != nil {
		t.Fatalf("bounded query: %v", err)
	}
	if len(got) != 1 || got[0].BlockNumber != 50 {
		t.Fatalf("want exactly block 50, got %+v", got)
	}
	// Must have seeked per block (bare primaryPrefix never used).
	for _, p := range kv.scannedPrefix {
		if p == primaryPrefix {
			t.Fatalf("from-zero scan of %q must not occur; prefixes=%v", primaryPrefix, kv.scannedPrefix)
		}
	}
	wantPrefixes := 3 // blocks 50, 51, 52
	if len(kv.scannedPrefix) != wantPrefixes {
		t.Fatalf("want %d per-block seeks, got %d (%v)", wantPrefixes, len(kv.scannedPrefix), kv.scannedPrefix)
	}
}

func TestGetLogs_ResultCapAddressless(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)
	addr := common.HexToAddress("0x03")
	// maxLogResults+1 logs inside a single in-range block (many log indexes).
	for i := 0; i <= maxLogResults; i++ {
		seedLog(t, s, 7, 0, uint(i), addr)
	}
	_, err := s.GetLogs(context.Background(), store.LogFilter{FromBlock: 7, ToBlock: 7})
	if !errors.Is(err, ErrTooManyResults) {
		t.Fatalf("want ErrTooManyResults, got %v", err)
	}
}

func TestGetLogs_AddressPathStillWorks(t *testing.T) {
	kv := newRecordKV()
	s := New(kv)
	a := common.HexToAddress("0x0a")
	b := common.HexToAddress("0x0b")
	seedLog(t, s, 10, 0, 0, a)
	seedLog(t, s, 11, 0, 0, b)
	seedLog(t, s, 12, 0, 0, a)
	// Address filter for `a` over an unbounded range is allowed (index is bounded
	// per contract) and returns only a's logs.
	got, err := s.GetLogs(context.Background(), store.LogFilter{Addresses: []common.Address{a}})
	if err != nil {
		t.Fatalf("address query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 logs for addr a, got %d", len(got))
	}
	for _, l := range got {
		if l.Address != a {
			t.Fatalf("leaked log for %s", l.Address.Hex())
		}
	}
}

// sanity: the primary key layout the seek loop reconstructs matches primaryKey().
func TestBlockPrefixMatchesPrimaryKey(t *testing.T) {
	pk := string(primaryKey(50, 0, 0))
	pref := fmt.Sprintf("%s%020d:", primaryPrefix, uint64(50))
	if !strings.HasPrefix(pk, pref) {
		t.Fatalf("primaryKey %q does not start with seek prefix %q", pk, pref)
	}
	// and the JSON round-trip the collector relies on
	raw, _ := json.Marshal(&ethtypes.Log{BlockNumber: 50, Topics: []common.Hash{}, Data: []byte{}})
	var l ethtypes.Log
	if err := json.Unmarshal(raw, &l); err != nil || l.BlockNumber != 50 {
		t.Fatalf("log json round-trip broken: %v", err)
	}
}
