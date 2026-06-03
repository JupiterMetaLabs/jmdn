package thebegateway_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"
	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"gossipnode/DB_OPs/thebegateway"
)

// ---- mockAppender ----

type appendCall struct {
	ns      string
	recType string
	value   []byte
}

type mockAppender struct {
	mu    sync.Mutex
	calls []appendCall
	err   error
}

func (m *mockAppender) Append(_ context.Context, record *core.CanonicalRecord) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, appendCall{
		ns:      record.Namespace,
		recType: record.Type,
		value:   record.Value,
	})
	return uint64(len(m.calls)), m.err
}

func (m *mockAppender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockAppender) lastCall() (appendCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return appendCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// ---- mockCache ----

type setCacheCall struct {
	key   string
	value []byte
	ttl   time.Duration
}

type mockCache struct {
	mu       sync.Mutex
	setCalls []setCacheCall
	setErr   error
	data     map[string][]byte
	getErr   error
}

func newMockCache() *mockCache {
	return &mockCache{data: make(map[string][]byte)}
}

func (m *mockCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls = append(m.setCalls, setCacheCall{key: key, value: value, ttl: ttl})
	if m.setErr != nil {
		return m.setErr
	}
	m.data[key] = value
	return nil
}

func (m *mockCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.data[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}

func (m *mockCache) Delete(_ context.Context, _ ...string) error           { return nil }
func (m *mockCache) Exists(_ context.Context, _ string) (bool, error)      { return false, nil }
func (m *mockCache) Keys(_ context.Context, _ string, _ int64) ([]string, error) {
	return nil, nil
}
func (m *mockCache) TTL(_ context.Context, _ string) (time.Duration, error) { return 0, nil }
func (m *mockCache) Close() error                                            { return nil }

func (m *mockCache) setCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.setCalls)
}

func (m *mockCache) lastSetKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.setCalls) == 0 {
		return ""
	}
	return m.setCalls[len(m.setCalls)-1].key
}

// ---- mockOutbox ----

type mockOutbox struct {
	mu           sync.Mutex
	enqueueErr   error
	enqueueCalls int
	nextEntries  []thebegateway.OutboxEntry
	nextErr      error
	ackCalls     []int64
	ackErr       error
	incrCalls    []int64
	incrErr      error
}

func (m *mockOutbox) Enqueue(_ context.Context, _ thebegateway.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueueCalls++
	return m.enqueueErr
}

func (m *mockOutbox) Next(_ context.Context, _ int) ([]thebegateway.OutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextErr != nil {
		return nil, m.nextErr
	}
	return m.nextEntries, nil
}

func (m *mockOutbox) Ack(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalls = append(m.ackCalls, id)
	return m.ackErr
}

func (m *mockOutbox) IncrementAttempts(_ context.Context, id int64, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrCalls = append(m.incrCalls, id)
	return m.incrErr
}

func (m *mockOutbox) ackCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ackCalls)
}

func (m *mockOutbox) incrCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.incrCalls)
}

// ---- mockKV ----

type kvCall struct {
	op    string // "worm" | "derived" | "get"
	key   []byte
	value []byte
}

type mockKV struct {
	mu       sync.Mutex
	calls    []kvCall
	wormErr  error
	derivErr error
	getErr   error
	getVal   []byte
}

func (m *mockKV) PutWorm(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "worm", key: key, value: value})
	return m.wormErr
}

func (m *mockKV) PutDerived(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "derived", key: key, value: value})
	return m.derivErr
}

func (m *mockKV) Get(key []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "get", key: key})
	return m.getVal, m.getErr
}

func (m *mockKV) lastCall() (kvCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return kvCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// ---- mockGateway (for OutboxWorker tests) ----

type writeCall struct {
	method string
	record interface{}
}

type mockGateway struct {
	mu    sync.Mutex
	calls []writeCall
	err   error
}

func (m *mockGateway) record(method string, r interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, writeCall{method: method, record: r})
	return m.err
}

func (m *mockGateway) WriteBlock(_ context.Context, b *thebegateway.BlockRecord) error {
	return m.record("WriteBlock", b)
}
func (m *mockGateway) WriteAccount(_ context.Context, a *thebegateway.AccountRecord) error {
	return m.record("WriteAccount", a)
}
func (m *mockGateway) WriteTransaction(_ context.Context, t *thebegateway.TransactionRecord) error {
	return m.record("WriteTransaction", t)
}
func (m *mockGateway) WriteSnapshot(_ context.Context, s *thebegateway.SnapshotRecord) error {
	return m.record("WriteSnapshot", s)
}
func (m *mockGateway) WriteZKProof(_ context.Context, z *thebegateway.ZKProofRecord) error {
	return m.record("WriteZKProof", z)
}
func (m *mockGateway) WriteL1Finality(_ context.Context, l *thebegateway.L1FinalityRecord) error {
	return m.record("WriteL1Finality", l)
}
func (m *mockGateway) WriteContractCode(_ context.Context, r *thebegateway.ContractCodeRecord) error {
	return m.record("WriteContractCode", r)
}
func (m *mockGateway) WriteContractNonce(_ context.Context, r *thebegateway.ContractNonceRecord) error {
	return m.record("WriteContractNonce", r)
}
func (m *mockGateway) WriteContractStorage(_ context.Context, r *thebegateway.ContractStorageRecord) error {
	return m.record("WriteContractStorage", r)
}
func (m *mockGateway) WriteContractMeta(_ context.Context, r *thebegateway.ContractMetaRecord) error {
	return m.record("WriteContractMeta", r)
}
func (m *mockGateway) WriteContractReceipt(_ context.Context, r *thebegateway.ContractReceiptRecord) error {
	return m.record("WriteContractReceipt", r)
}

func (m *mockGateway) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockGateway) lastMethod() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return ""
	}
	return m.calls[len(m.calls)-1].method
}

// sentinel errors
var (
	errAppend   = errors.New("append failed")
	errOutbox   = errors.New("outbox failed")
	errDispatch = errors.New("dispatch failed")
)
