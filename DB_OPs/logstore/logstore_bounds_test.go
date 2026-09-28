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
