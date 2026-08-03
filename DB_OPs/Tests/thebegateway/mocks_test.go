package thebegateway_test

import (
	"context"
	"errors"
	"sync"
	"time"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"gossipnode/DB_OPs/thebegateway"
)

// ---- spyAppender ----

type appendCall struct {
	ns      string
	recType string
	value   []byte
}

type spyAppender struct {
	mu    sync.Mutex
	calls []appendCall
	err   error
}

func (m *spyAppender) Append(_ context.Context, record *core.CanonicalRecord) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, appendCall{
		ns:      record.Namespace,
		recType: record.Type,
		value:   record.Value,
	})
	return uint64(len(m.calls)), m.err
}

func (m *spyAppender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *spyAppender) lastCall() (appendCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return appendCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// ---- spyCache ----

type setCacheCall struct {
	key   string
	value []byte
	ttl   time.Duration
}

type spyCache struct {
	mu       sync.Mutex
	setCalls []setCacheCall
	setErr   error
	data     map[string][]byte
}

func newSpyCache() *spyCache {
	return &spyCache{data: make(map[string][]byte)}
}

func (m *spyCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls = append(m.setCalls, setCacheCall{key: key, value: value, ttl: ttl})
	if m.setErr != nil {
		return m.setErr
	}
	m.data[key] = value
	return nil
}

func (m *spyCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, errors.New("cache: miss")
	}
	return v, nil
}

func (m *spyCache) Delete(_ context.Context, _ ...string) error               { return nil }
func (m *spyCache) Exists(_ context.Context, _ string) (bool, error)          { return false, nil }
func (m *spyCache) Keys(_ context.Context, _ string, _ int64) ([]string, error) {
	return nil, nil
}
func (m *spyCache) TTL(_ context.Context, _ string) (time.Duration, error) { return 0, nil }
func (m *spyCache) Close() error                                             { return nil }

func (m *spyCache) setCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.setCalls)
}

func (m *spyCache) lastSetKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.setCalls) == 0 {
		return ""
	}
	return m.setCalls[len(m.setCalls)-1].key
}

// ---- spyOutbox ----

type spyOutbox struct {
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

func (m *spyOutbox) Enqueue(_ context.Context, _ thebegateway.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueueCalls++
	return m.enqueueErr
}

func (m *spyOutbox) Next(_ context.Context, _ int) ([]thebegateway.OutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextErr != nil {
		return nil, m.nextErr
	}
	return m.nextEntries, nil
}

func (m *spyOutbox) Ack(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalls = append(m.ackCalls, id)
	return m.ackErr
}

func (m *spyOutbox) IncrementAttempts(_ context.Context, id int64, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrCalls = append(m.incrCalls, id)
	return m.incrErr
}

func (m *spyOutbox) ackCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ackCalls)
}

func (m *spyOutbox) incrCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.incrCalls)
}

// ---- spyKV ----

type kvCall struct {
	op    string // "worm" | "derived" | "get"
	key   []byte
	value []byte
}

type spyKV struct {
	mu       sync.Mutex
	calls    []kvCall
	wormErr  error
	derivErr error
	getErr   error
	getVal   []byte
}

func (m *spyKV) PutWorm(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "worm", key: key, value: value})
	return m.wormErr
}

func (m *spyKV) PutDerived(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "derived", key: key, value: value})
	return m.derivErr
}

func (m *spyKV) Get(key []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, kvCall{op: "get", key: key})
	return m.getVal, m.getErr
}

func (m *spyKV) lastCall() (kvCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return kvCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// ---- spyGateway (for OutboxWorker tests) ----

type writeCall struct {
	method string
	record any
}

type spyGateway struct {
	mu    sync.Mutex
	calls []writeCall
	err   error
}

func (m *spyGateway) rec(method string, r any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, writeCall{method: method, record: r})
	return m.err
}

func (m *spyGateway) WriteBlock(_ context.Context, b *thebegateway.BlockRecord) error {
	return m.rec("WriteBlock", b)
}
func (m *spyGateway) WriteAccount(_ context.Context, a *thebegateway.AccountRecord) error {
	return m.rec("WriteAccount", a)
}
func (m *spyGateway) WriteTransaction(_ context.Context, t *thebegateway.TransactionRecord) error {
	return m.rec("WriteTransaction", t)
}
func (m *spyGateway) WriteSnapshot(_ context.Context, s *thebegateway.SnapshotRecord) error {
	return m.rec("WriteSnapshot", s)
}
func (m *spyGateway) WriteZKProof(_ context.Context, z *thebegateway.ZKProofRecord) error {
	return m.rec("WriteZKProof", z)
}
func (m *spyGateway) WriteL1Finality(_ context.Context, l *thebegateway.L1FinalityRecord) error {
	return m.rec("WriteL1Finality", l)
}
func (m *spyGateway) WriteContractCode(_ context.Context, r *thebegateway.ContractCodeRecord) error {
	return m.rec("WriteContractCode", r)
}
func (m *spyGateway) WriteContractNonce(_ context.Context, r *thebegateway.ContractNonceRecord) error {
	return m.rec("WriteContractNonce", r)
}
func (m *spyGateway) WriteContractStorage(_ context.Context, r *thebegateway.ContractStorageRecord) error {
	return m.rec("WriteContractStorage", r)
}
func (m *spyGateway) WriteContractMeta(_ context.Context, r *thebegateway.ContractMetaRecord) error {
	return m.rec("WriteContractMeta", r)
}
func (m *spyGateway) WriteContractReceipt(_ context.Context, r *thebegateway.ContractReceiptRecord) error {
	return m.rec("WriteContractReceipt", r)
}
func (m *spyGateway) SetTxProcessing(_ context.Context, txHash string) error {
	return m.rec("SetTxProcessing", txHash)
}
func (m *spyGateway) ClearTxProcessing(_ context.Context, txHash string) error {
	return m.rec("ClearTxProcessing", txHash)
}

func (m *spyGateway) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *spyGateway) lastMethod() string {
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
