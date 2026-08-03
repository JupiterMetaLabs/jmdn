package thebegateway_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"
	"gossipnode/DB_OPs/thebegateway"
)

// mockCache is an in-memory cache.Cache for unit tests.
// Only Get and Set are exercised by the read-through path; all other methods are no-ops.
type mockCache struct {
	data      map[string][]byte
	setCalled int
}

func (m *mockCache) Get(_ context.Context, key string) ([]byte, error) {
	if v, ok := m.data[key]; ok {
		return v, nil
	}
	return nil, cache.ErrMiss
}

func (m *mockCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.setCalled++
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[key] = value
	return nil
}

func (m *mockCache) Delete(_ context.Context, _ ...string) error                 { return nil }
func (m *mockCache) Exists(_ context.Context, _ string) (bool, error)            { return false, nil }
func (m *mockCache) Keys(_ context.Context, _ string, _ int64) ([]string, error) { return nil, nil }
func (m *mockCache) TTL(_ context.Context, _ string) (time.Duration, error)      { return 0, nil }
func (m *mockCache) Close() error                                                { return nil }

// mockKV is an in-memory ThebeKVStore for unit tests.
type mockKV struct {
	data map[string][]byte
}

func (m *mockKV) Get(key []byte) ([]byte, error) {
	v, ok := m.data[string(key)]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}

func (m *mockKV) PutWorm(key, value []byte) error    { m.data[string(key)] = value; return nil }
func (m *mockKV) PutDerived(key, value []byte) error { m.data[string(key)] = value; return nil }

// newCacheWithJSON stores a JSON-marshalled value at key in a fresh mockCache.
func newCacheWithJSON(t *testing.T, key string, v any) *mockCache {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return &mockCache{data: map[string][]byte{key: b}}
}

// newReader constructs a ThebeReader with a nil *sql.DB; safe as long as SQL path is never hit.
func newReader(kv thebegateway.ThebeKVStore, c cache.Cache) thebegateway.ThebeReader {
	return thebegateway.NewThebeReader((*sql.DB)(nil), kv, c)
}

// --- Cache-hit tests for SQL-backed entities ---
// These pass nil *sql.DB; a bug that reaches SQL panics, proving the cache path is taken.

func TestGetBlock_CacheHit(t *testing.T) {
	want := thebegateway.BlockRecord{BlockNumber: 42, BlockHash: "0xabcd", ParentHash: "0xparent", Status: 1}
	c := newCacheWithJSON(t, thebegateway.BlockKey(42), want)
	r := newReader(nil, c)

	got, err := r.GetBlock(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if got.BlockNumber != want.BlockNumber {
		t.Errorf("BlockNumber: want %d, got %d", want.BlockNumber, got.BlockNumber)
	}
	if got.BlockHash != want.BlockHash {
		t.Errorf("BlockHash: want %q, got %q", want.BlockHash, got.BlockHash)
	}
}

func TestGetAccount_CacheHit(t *testing.T) {
	want := thebegateway.AccountRecord{Address: "0xabc", BalanceWei: "1000", Nonce: "7"}
	c := newCacheWithJSON(t, thebegateway.AccountKey("0xabc"), want)
	r := newReader(nil, c)

	got, err := r.GetAccount(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Address != want.Address {
		t.Errorf("Address: want %q, got %q", want.Address, got.Address)
	}
	if got.BalanceWei != want.BalanceWei {
		t.Errorf("BalanceWei: want %q, got %q", want.BalanceWei, got.BalanceWei)
	}
}

func TestGetTransaction_CacheHit(t *testing.T) {
	toAddr := "0xto"
	want := thebegateway.TransactionRecord{TxHash: "0xtxhash", BlockNumber: 1, FromAddr: "0xfrom", ToAddr: &toAddr}
	c := newCacheWithJSON(t, thebegateway.TransactionKey("0xtxhash"), want)
	r := newReader(nil, c)

	got, err := r.GetTransaction(context.Background(), "0xtxhash")
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if got.TxHash != want.TxHash {
		t.Errorf("TxHash: want %q, got %q", want.TxHash, got.TxHash)
	}
	if got.ToAddr == nil || *got.ToAddr != toAddr {
		t.Errorf("ToAddr: want %q", toAddr)
	}
}

func TestGetLatestBlock_CacheHit(t *testing.T) {
	want := thebegateway.BlockRecord{BlockNumber: 99, BlockHash: "0xlatest"}
	c := newCacheWithJSON(t, thebegateway.LatestBlockKey(), want)
	r := newReader(nil, c)

	got, err := r.GetLatestBlock(context.Background())
	if err != nil {
		t.Fatalf("GetLatestBlock: %v", err)
	}
	if got.BlockNumber != want.BlockNumber {
		t.Errorf("BlockNumber: want %d, got %d", want.BlockNumber, got.BlockNumber)
	}
}

func TestGetZKProof_CacheHit(t *testing.T) {
	want := thebegateway.ZKProofRecord{BlockNumber: 7, ProofHash: "0xproof", StarkProof: []byte("stark")}
	c := newCacheWithJSON(t, thebegateway.ZKProofKey(7), want)
	r := newReader(nil, c)

	got, err := r.GetZKProof(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetZKProof: %v", err)
	}
	if got.BlockNumber != want.BlockNumber {
		t.Errorf("BlockNumber: want %d, got %d", want.BlockNumber, got.BlockNumber)
	}
	if got.ProofHash != want.ProofHash {
		t.Errorf("ProofHash: want %q, got %q", want.ProofHash, got.ProofHash)
	}
}

func TestGetSnapshot_CacheHit(t *testing.T) {
	want := thebegateway.SnapshotRecord{BlockNumber: 7, BlockHash: "0xsnaphash"}
	c := newCacheWithJSON(t, thebegateway.SnapshotKey(7), want)
	r := newReader(nil, c)

	got, err := r.GetSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if got.BlockNumber != want.BlockNumber {
		t.Errorf("BlockNumber: want %d, got %d", want.BlockNumber, got.BlockNumber)
	}
}

// --- KV-backed entity tests ---

func TestGetContractCode_KV(t *testing.T) {
	addr := "0x" + strings.Repeat("aa", 20)
	want := thebegateway.ContractCodeRecord{Address: addr, Code: []byte{0x60, 0x80}}

	b, _ := json.Marshal(want)
	kv := &mockKV{data: map[string][]byte{"contract:code:" + addr: b}}
	r := newReader(kv, nil)

	got, err := r.GetContractCode(context.Background(), addr)
	if err != nil {
		t.Fatalf("GetContractCode: %v", err)
	}
	if got.Address != want.Address {
		t.Errorf("Address: want %q, got %q", want.Address, got.Address)
	}
	if string(got.Code) != string(want.Code) {
		t.Errorf("Code: want %v, got %v", want.Code, got.Code)
	}
}

func TestGetContractNonce_KV(t *testing.T) {
	addr := "0x" + strings.Repeat("bb", 20)

	// nonce stored as raw big-endian uint64
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, 42)
	kv := &mockKV{data: map[string][]byte{"contract:nonce:" + addr: raw}}
	r := newReader(kv, nil)

	got, err := r.GetContractNonce(context.Background(), addr)
	if err != nil {
		t.Fatalf("GetContractNonce: %v", err)
	}
	if got.Nonce != 42 {
		t.Errorf("Nonce: want 42, got %d", got.Nonce)
	}
}

func TestGetContractStorage_KV(t *testing.T) {
	addr := "0x" + strings.Repeat("cc", 20)
	slot := make([]byte, 32) // 32-byte slot, all zeros

	want := thebegateway.ContractStorageRecord{Address: addr, Slot: "0x" + strings.Repeat("00", 32), ValueHash: "0xval"}

	// Build the binary key: "contract:storage:" + addr_20_raw + slot_32_raw
	addrRaw := make([]byte, 20)
	for i := range addrRaw {
		addrRaw[i] = 0xcc
	}
	prefix := "contract:storage:"
	kvKey := make([]byte, len(prefix)+20+32)
	n := copy(kvKey, prefix)
	n += copy(kvKey[n:], addrRaw)
	copy(kvKey[n:], slot)

	// Verify expected key length
	if len(kvKey) != 69 {
		t.Fatalf("kvKey length: want 69, got %d", len(kvKey))
	}

	b, _ := json.Marshal(want)
	kv := &mockKV{data: map[string][]byte{string(kvKey): b}}
	r := newReader(kv, nil)

	got, err := r.GetContractStorage(context.Background(), addr, slot)
	if err != nil {
		t.Fatalf("GetContractStorage: %v", err)
	}
	if got.Address != want.Address {
		t.Errorf("Address: want %q, got %q", want.Address, got.Address)
	}
	if got.ValueHash != want.ValueHash {
		t.Errorf("ValueHash: want %q, got %q", want.ValueHash, got.ValueHash)
	}
}

func TestGetContractStorage_InvalidAddress(t *testing.T) {
	slot := make([]byte, 32)
	r := newReader(&mockKV{data: map[string][]byte{}}, nil)

	_, err := r.GetContractStorage(context.Background(), "notvalidhex", slot)
	if err == nil {
		t.Fatal("expected error for invalid address, got nil")
	}
}

func TestGetContractReceipt_CacheHit(t *testing.T) {
	contractAddr := "0xcontract"
	want := thebegateway.ContractReceiptRecord{
		TxHash:          "0xtxhash",
		BlockNumber:     5,
		Status:          1,
		GasUsed:         "21000",
		ContractAddress: &contractAddr,
	}
	c := newCacheWithJSON(t, thebegateway.ContractReceiptKey("0xtxhash"), want)
	r := newReader(nil, c)

	got, err := r.GetContractReceipt(context.Background(), "0xtxhash")
	if err != nil {
		t.Fatalf("GetContractReceipt: %v", err)
	}
	if got.TxHash != want.TxHash {
		t.Errorf("TxHash: want %q, got %q", want.TxHash, got.TxHash)
	}
	if got.Status != want.Status {
		t.Errorf("Status: want %d, got %d", want.Status, got.Status)
	}
	if got.ContractAddress == nil || *got.ContractAddress != contractAddr {
		t.Errorf("ContractAddress: want %q", contractAddr)
	}
}

// ContractReceiptKey and TransactionKey must not collide for the same hash.
func TestGetContractReceipt_CacheKeyDistinct(t *testing.T) {
	hash := "0xabc"
	if thebegateway.ContractReceiptKey(hash) == thebegateway.TransactionKey(hash) {
		t.Errorf("ContractReceiptKey and TransactionKey must differ for hash %q", hash)
	}
}
