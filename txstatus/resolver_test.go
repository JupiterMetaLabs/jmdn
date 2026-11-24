package txstatus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

// fakeChain is a chain store whose answer can change between calls, which is
// what makes the C3 "mined between the two reads" case testable.
type fakeChain struct {
	mu sync.Mutex
	// answers is consumed one entry per call; the last entry repeats.
	answers []chainAnswer
	calls   int
}

type chainAnswer struct {
	mined bool
	err   error
}

func chainAlways(mined bool) *fakeChain {
	return &fakeChain{answers: []chainAnswer{{mined: mined}}}
}

func chainSeq(answers ...chainAnswer) *fakeChain {
	return &fakeChain{answers: answers}
}

func (c *fakeChain) IsMined(_ context.Context, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	idx := c.calls
	c.calls++
	if idx >= len(c.answers) {
		idx = len(c.answers) - 1
	}
	a := c.answers[idx]
	return a.mined, a.err
}

func (c *fakeChain) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// fakeMempool records every lookup so tests can assert the mempool was not
// consulted at all on paths that must not touch it.
type fakeMempool struct {
	mu     sync.Mutex
	result *MempoolResult
	err    error
	delay  time.Duration
	calls  int
	hashes []string
}

func (m *fakeMempool) Lookup(ctx context.Context, hash string) (*MempoolResult, error) {
	m.mu.Lock()
	m.calls++
	m.hashes = append(m.hashes, hash)
	delay, res, err := m.delay, m.result, m.err
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return res, err
}

func (m *fakeMempool) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type fakeFailed struct {
	rec *FailedRecord
}

func (f *fakeFailed) Get(_ context.Context, _ string) (*FailedRecord, bool) {
	if f.rec == nil {
		return nil, false
	}
	return f.rec, true
}

const (
	testHash    = "0xaaaa000000000000000000000000000000000000000000000000000000000001"
	unknownHash = "0xffff00000000000000000000000000000000000000000000000000000000dead"
)

func queuedResult() *MempoolResult {
	return &MempoolResult{
		Found:   true,
		ShardID: 3,
		NodeID:  "mempool-03",
		Tx:      &PendingTx{Hash: testHash, From: "0xsender", Nonce: 7},
	}
}

func absentResult() *MempoolResult { return &MempoolResult{Found: false, Degraded: false} }

func degradedResult() *MempoolResult {
	return &MempoolResult{Found: false, Degraded: true, Detail: "2/4 shards failed"}
}

// baseCfg gives generous timeouts and disables the guards, so a test that is
// not about a guard cannot be perturbed by one.
func baseCfg() Config {
	return Config{
		MempoolTimeout: 2 * time.Second,
		ChainTimeout:   2 * time.Second,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T1 — a mined transaction is answered from the chain store, with ZERO
// mempool calls. The cheap path must stay cheap.
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_MinedShortCircuitsBeforeMempool(t *testing.T) {
	chain := chainAlways(true)
	mem := &fakeMempool{result: queuedResult()}

	r := NewResolver(Deps{Chain: chain, Mempool: mem, Config: baseCfg()})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusMined {
		t.Errorf("status = %q, want %q", res.Status, StatusMined)
	}
	if res.Source != SourceChain {
		t.Errorf("source = %q, want %q", res.Source, SourceChain)
	}
	if got := mem.callCount(); got != 0 {
		t.Errorf("mempool was consulted %d times for a mined transaction; want 0", got)
	}
	if got := chain.callCount(); got != 1 {
		t.Errorf("chain reads = %d, want 1", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T2 — queued in the mempool
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_QueuedFromMempool(t *testing.T) {
	r := NewResolver(Deps{
		Chain:   chainAlways(false),
		Mempool: &fakeMempool{result: queuedResult()},
		Config:  baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusQueued {
		t.Fatalf("status = %q, want %q", res.Status, StatusQueued)
	}
	if res.Source != SourceMempool {
		t.Errorf("source = %q, want %q", res.Source, SourceMempool)
	}
	if res.MempoolNode != "mempool-03" {
		t.Errorf("mempool_node = %q, want mempool-03", res.MempoolNode)
	}
	if res.ShardID == nil || *res.ShardID != 3 {
		t.Errorf("shard_id = %v, want 3", res.ShardID)
	}
	if res.Tx == nil {
		t.Error("queued result carries no transaction body; eth_getTransactionByHash cannot answer from it")
	}
	if res.Degraded {
		t.Error("a clean mempool hit must not be marked degraded")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T9 / C3 — mined between the first chain read and the mempool hit.
//
// Destructive mempool fetches delete asynchronously, so the mempool can report
// a transaction that is already in a block. The second chain read must win;
// otherwise we intermittently report `queued` for mined transactions.
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_MinedBetweenReadsReportsMinedNotQueued(t *testing.T) {
	chain := chainSeq(
		chainAnswer{mined: false}, // first read: not yet in a block
		chainAnswer{mined: true},  // re-check: it landed while we asked the mempool
	)
	mem := &fakeMempool{result: queuedResult()}

	r := NewResolver(Deps{Chain: chain, Mempool: mem, Config: baseCfg()})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusMined {
		t.Fatalf("status = %q, want %q — the post-mempool chain re-check did not win", res.Status, StatusMined)
	}
	if res.Source != SourceChain {
		t.Errorf("source = %q, want %q", res.Source, SourceChain)
	}
	if got := chain.callCount(); got != 2 {
		t.Errorf("chain reads = %d, want 2 (the C3 re-check is missing)", got)
	}
}

func TestResolve_ReCheckFailureFallsBackToQueued(t *testing.T) {
	// If the re-check itself errors we still have a positive mempool hit, so
	// `queued` remains the best available answer — the re-check exists to catch
	// a race, not to veto the mempool.
	chain := chainSeq(
		chainAnswer{mined: false},
		chainAnswer{err: errors.New("db blip")},
	)
	r := NewResolver(Deps{
		Chain:   chain,
		Mempool: &fakeMempool{result: queuedResult()},
		Config:  baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusQueued {
		t.Errorf("status = %q, want %q", res.Status, StatusQueued)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T4 — an unknown hash is `unknown`, never `processing`
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_UnknownHashIsUnknownNotProcessing(t *testing.T) {
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		SubmitLog: NewSubmitLog(time.Hour, 100), // empty: we never saw this hash
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), unknownHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — reporting processing for a hash we never saw makes a wallet poll forever", res.Status, StatusUnknown)
	}
	if res.Degraded {
		t.Error("a fully answered miss must be conclusive, not degraded")
	}
}

func TestResolve_EmptyHashRejected(t *testing.T) {
	r := NewResolver(Deps{Chain: chainAlways(false), Config: baseCfg()})
	if _, err := r.Resolve(context.Background(), "   "); err == nil {
		t.Error("expected an error for an empty hash")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T5 — submitted but not yet visible: `processing`, from the submit log
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_ProcessingFromSubmitLog(t *testing.T) {
	log := NewSubmitLog(time.Hour, 100)
	log.Record(SubmitRecord{Hash: testHash, Sender: "0xsender", Nonce: 4, Forwarded: true})

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		SubmitLog: log,
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusProcessing {
		t.Fatalf("status = %q, want %q", res.Status, StatusProcessing)
	}
	if res.Source != SourceSubmitLog {
		t.Errorf("source = %q, want %q", res.Source, SourceSubmitLog)
	}
	if res.SubmittedAt == nil {
		t.Error("processing result should carry submitted_at")
	}
}

// A forward that failed means the transaction never reached the mempool and
// will never be mined. Reporting `processing` would leave a wallet polling
// forever on something that does not exist anywhere.
func TestResolve_FailedForwardIsUnknownNotProcessing(t *testing.T) {
	log := NewSubmitLog(time.Hour, 100)
	log.Record(SubmitRecord{
		Hash:       testHash,
		Forwarded:  false,
		ForwardErr: "mempool client not initialized",
	})

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		SubmitLog: log,
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q", res.Status, StatusUnknown)
	}
	if res.Detail == "" {
		t.Error("a failed forward must explain itself in detail")
	}
}

// An expired submit record must not keep reporting `processing` forever.
func TestResolve_ExpiredSubmitRecordDegradesToUnknown(t *testing.T) {
	log := NewSubmitLog(50*time.Millisecond, 100)
	now := time.Now()
	log.now = func() time.Time { return now }
	log.Record(SubmitRecord{Hash: testHash, Forwarded: true})

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		SubmitLog: log,
		Config:    baseCfg(),
	})

	if res, _ := r.Resolve(context.Background(), testHash); res.Status != StatusProcessing {
		t.Fatalf("before expiry: status = %q, want %q", res.Status, StatusProcessing)
	}

	now = now.Add(time.Second)
	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusUnknown {
		t.Errorf("after expiry: status = %q, want %q", res.Status, StatusUnknown)
	}
}

// A degraded mempool answer must NOT cancel the submit log's evidence. The log
// is independent proof the transaction exists; "the mempool could not answer"
// is not evidence against it.
func TestResolve_DegradedMempoolStillReportsProcessingWhenSubmitted(t *testing.T) {
	log := NewSubmitLog(time.Hour, 100)
	log.Record(SubmitRecord{Hash: testHash, Forwarded: true})

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: degradedResult()},
		SubmitLog: log,
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusProcessing {
		t.Fatalf("status = %q, want %q", res.Status, StatusProcessing)
	}
	if !res.Degraded {
		t.Error("the result should be flagged degraded so a caller knows the mempool could not confirm")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T6 — a recorded rejection resolves to `failed`
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_FailedFromFailedStore(t *testing.T) {
	r := NewResolver(Deps{
		Chain:   chainAlways(false),
		Mempool: &fakeMempool{result: absentResult()},
		Failed: &fakeFailed{rec: &FailedRecord{
			Hash:        testHash,
			Reason:      "nonce too low",
			MempoolNode: "mempool-02",
		}},
		Config: baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", res.Status, StatusFailed)
	}
	if res.Reason != "nonce too low" {
		t.Errorf("reason = %q, want %q", res.Reason, "nonce too low")
	}
}

// A nil FailedStore is the shipped configuration: rejections are not yet
// delivered to jmdn. It must make `failed` unreachable without breaking
// anything else — never a wrong `failed`, never a crash.
func TestResolve_NilFailedStoreIsSafe(t *testing.T) {
	log := NewSubmitLog(time.Hour, 100)
	log.Record(SubmitRecord{Hash: testHash, Forwarded: true})

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		Failed:    nil,
		SubmitLog: log,
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), testHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusProcessing {
		t.Errorf("status = %q, want %q", res.Status, StatusProcessing)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T7 — mempool unreachable: chain truth, no hang, no error propagated
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_MempoolErrorDoesNotErrorTheQuery(t *testing.T) {
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{err: errors.New("connection refused")},
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), unknownHash)
	if err != nil {
		t.Fatalf("an unreachable mempool must not error the status query: %v", err)
	}
	if res.Status != StatusUnknown {
		t.Errorf("status = %q, want %q", res.Status, StatusUnknown)
	}
	if !res.Degraded {
		t.Error("an unreachable mempool cannot produce a conclusive answer; want degraded=true")
	}
}

func TestResolve_MempoolTimeoutDegradesQuicklyAndDoesNotHang(t *testing.T) {
	cfg := baseCfg()
	cfg.MempoolTimeout = 80 * time.Millisecond

	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{delay: 5 * time.Second, result: queuedResult()},
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    cfg,
	})

	start := time.Now()
	res, err := r.Resolve(context.Background(), testHash)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("resolve blew through its mempool timeout (%s); a status query must never hang an RPC handler", elapsed)
	}
	if res.Status != StatusUnknown || !res.Degraded {
		t.Errorf("status = %q degraded = %v; want unknown/degraded", res.Status, res.Degraded)
	}
}

func TestResolve_NilMempoolIsDegradedNotAbsent(t *testing.T) {
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   nil,
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    baseCfg(),
	})

	res, err := r.Resolve(context.Background(), unknownHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Degraded {
		t.Error("with no mempool configured we cannot prove absence; want degraded=true")
	}
}

// The chain store is the one dependency that cannot be worked around: guessing
// "not mined" on a database failure could contradict a block that exists.
func TestResolve_ChainErrorIsReturned(t *testing.T) {
	mem := &fakeMempool{result: queuedResult()}
	r := NewResolver(Deps{
		Chain:   chainAlways(false),
		Mempool: mem,
		Config:  baseCfg(),
	})
	r.chain = chainSeq(chainAnswer{err: errors.New("db down")})

	if _, err := r.Resolve(context.Background(), testHash); err == nil {
		t.Fatal("expected a chain-store error to surface")
	}
	if got := mem.callCount(); got != 0 {
		t.Errorf("mempool consulted %d times after a chain-store failure; want 0", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// T12 / C4 — amplification bounds
// ─────────────────────────────────────────────────────────────────────────────

func TestResolve_NegativeCacheBoundsRepeatedUnknownProbes(t *testing.T) {
	cfg := baseCfg()
	cfg.NegativeCacheTTL = time.Minute
	cfg.NegativeCacheSize = 1000

	mem := &fakeMempool{result: absentResult()}
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   mem,
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    cfg,
	})

	const probes = 40
	for i := 0; i < probes; i++ {
		res, err := r.Resolve(context.Background(), unknownHash)
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		if res.Status != StatusUnknown {
			t.Fatalf("probe %d: status = %q", i, res.Status)
		}
	}

	if got := mem.callCount(); got != 1 {
		t.Errorf("%d probes produced %d mempool lookups; want 1 (negative cache is not holding)", probes, got)
	}
}

// A degraded answer must never be cached: caching it would pin a genuinely
// pending transaction to `unknown` for the whole TTL because one shard timed
// out.
func TestResolve_DegradedAnswerIsNotCached(t *testing.T) {
	cfg := baseCfg()
	cfg.NegativeCacheTTL = time.Minute
	cfg.NegativeCacheSize = 1000

	mem := &fakeMempool{result: degradedResult()}
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   mem,
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    cfg,
	})

	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), unknownHash); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}

	if r.negCache.len() != 0 {
		t.Error("a degraded answer was written to the negative cache")
	}
	if got := mem.callCount(); got != 3 {
		t.Errorf("mempool lookups = %d, want 3 — a degraded answer must not be served from cache", got)
	}
}

func TestResolve_RateLimitDegradesWithoutCallingMempool(t *testing.T) {
	cfg := baseCfg()
	cfg.RateLimitPerSec = 0.001 // effectively no refill during the test
	cfg.RateLimitBurst = 2

	mem := &fakeMempool{result: absentResult()}
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   mem,
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    cfg,
	})

	for i := 0; i < 2; i++ {
		if _, err := r.Resolve(context.Background(), fmt.Sprintf("0xhash%02d", i)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := mem.callCount(); got != 2 {
		t.Fatalf("mempool lookups within burst = %d, want 2", got)
	}

	res, err := r.Resolve(context.Background(), "0xhash99")
	if err != nil {
		t.Fatalf("a rate-limited status query must not error: %v", err)
	}
	if !res.Degraded {
		t.Error("a rate-limited lookup cannot prove absence; want degraded=true")
	}
	if got := mem.callCount(); got != 2 {
		t.Errorf("mempool lookups after the limit = %d, want 2 (the limiter let a call through)", got)
	}
}

func TestResolve_BreakerStopsCallingAnUnresponsiveMempool(t *testing.T) {
	cfg := baseCfg()
	cfg.BreakerFailureThreshold = 3
	cfg.BreakerCooldown = time.Minute

	mem := &fakeMempool{err: errors.New("unreachable")}
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   mem,
		SubmitLog: NewSubmitLog(time.Hour, 100),
		Config:    cfg,
	})

	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), fmt.Sprintf("0xprobe%d", i)); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	callsAtTrip := mem.callCount()
	if callsAtTrip != 3 {
		t.Fatalf("mempool lookups before trip = %d, want 3", callsAtTrip)
	}

	res, err := r.Resolve(context.Background(), "0xprobe-after-trip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Degraded {
		t.Error("an open breaker must produce a degraded result")
	}
	if got := mem.callCount(); got != callsAtTrip {
		t.Errorf("mempool lookups after trip = %d, want %d — the open breaker still called out", got, callsAtTrip)
	}
	if r.breaker.tripCount() != 1 {
		t.Errorf("breaker trips = %d, want 1", r.breaker.tripCount())
	}
}

func TestResolve_BreakerStaysClosedWhileHealthy(t *testing.T) {
	cfg := baseCfg()
	cfg.BreakerFailureThreshold = 2
	cfg.BreakerCooldown = time.Minute

	r := NewResolver(Deps{
		Chain:   chainAlways(false),
		Mempool: &fakeMempool{result: queuedResult()},
		Config:  cfg,
	})

	for i := 0; i < 5; i++ {
		res, err := r.Resolve(context.Background(), testHash)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if res.Status != StatusQueued {
			t.Fatalf("call %d: status = %q", i, res.Status)
		}
	}
	if r.breaker.tripCount() != 0 {
		t.Errorf("breaker tripped %d times on a healthy mempool", r.breaker.tripCount())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Hash handling and concurrency
// ─────────────────────────────────────────────────────────────────────────────

// The resolver keys its own stores on a normalised hash, but must send the
// mempool exactly what it was asked about — the mempool indexes on the string
// the transaction was submitted with.
func TestResolve_NormalisesHashForLocalStoresAndForwardsIt(t *testing.T) {
	log := NewSubmitLog(time.Hour, 100)
	log.Record(SubmitRecord{Hash: "0xABCD", Forwarded: true})

	mem := &fakeMempool{result: absentResult()}
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   mem,
		SubmitLog: log,
		Config:    baseCfg(),
	})

	// Different casing, no prefix — must still find the record.
	res, err := r.Resolve(context.Background(), "abcd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != StatusProcessing {
		t.Errorf("status = %q, want %q — hash normalisation is not consistent", res.Status, StatusProcessing)
	}
	if res.Hash != "0xabcd" {
		t.Errorf("result hash = %q, want 0xabcd", res.Hash)
	}
}

func TestResolve_ConcurrentUseIsRaceFree(t *testing.T) {
	cfg := baseCfg()
	cfg.NegativeCacheTTL = time.Second
	cfg.NegativeCacheSize = 256
	cfg.BreakerFailureThreshold = 3
	cfg.BreakerCooldown = 10 * time.Millisecond
	cfg.RateLimitPerSec = 10000
	cfg.RateLimitBurst = 10000

	log := NewSubmitLog(time.Minute, 512)
	r := NewResolver(Deps{
		Chain:     chainAlways(false),
		Mempool:   &fakeMempool{result: absentResult()},
		SubmitLog: log,
		Config:    cfg,
		Observer:  countingObserver{},
	})

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				h := fmt.Sprintf("0x%02x%02x", i, j)
				log.Record(SubmitRecord{Hash: h, Forwarded: j%2 == 0})
				if _, err := r.Resolve(context.Background(), h); err != nil {
					t.Errorf("resolve: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// countingObserver exercises the Observer path under -race without asserting
// on counts (the metrics implementation is tested by using it, not by this).
type countingObserver struct{}

func (countingObserver) ObserveResolve(string, string, bool, time.Duration) {}
func (countingObserver) ObserveMempoolLookup(string, time.Duration)         {}
func (countingObserver) ObserveBreakerTrips(int64)                          {}
func (countingObserver) ObserveNegativeCache(string)                        {}
