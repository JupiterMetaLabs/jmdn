package thebegateway_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gossipnode/DB_OPs/thebegateway"
)

// oneShotOutbox wraps spyOutbox so Next returns entries exactly once.
// Subsequent Next calls return nothing — prevents repeated ticker ticks from
// replaying the same entries.
type oneShotOutbox struct {
	spyOutbox
	consumed bool
}

func (o *oneShotOutbox) Next(_ context.Context, _ int) ([]thebegateway.OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.nextErr != nil {
		return nil, o.nextErr
	}
	if o.consumed {
		return nil, nil
	}
	o.consumed = true
	return o.nextEntries, nil
}

// drainOnce starts the worker with a 1ms ticker, waits for at least one drain
// cycle, then stops. Uses oneShotOutbox so the same entries aren't replayed on
// every tick during the sleep window.
func drainOnce(spy *spyOutbox, gw *spyGateway) {
	oso := &oneShotOutbox{}
	oso.nextEntries = spy.nextEntries
	oso.ackErr = spy.ackErr
	oso.incrErr = spy.incrErr
	oso.enqueueErr = spy.enqueueErr

	w := thebegateway.NewOutboxWorker(oso, gw, time.Millisecond)
	w.Start()
	time.Sleep(20 * time.Millisecond)
	w.Stop()

	// copy observed call slices back so callers can assert on them
	spy.ackCalls = oso.ackCalls
	spy.incrCalls = oso.incrCalls
	spy.enqueueCalls = oso.enqueueCalls
}

// TestDrainBatch_Success verifies Ack is called with the correct ID on dispatch success.
func TestDrainBatch_Success(t *testing.T) {
	payload, _ := json.Marshal(thebegateway.BlockRecord{BlockNumber: 1})
	store := &spyOutbox{
		nextEntries: []thebegateway.OutboxEntry{
			{ID: 101, Namespace: thebegateway.NamespaceBlock, Payload: payload, Attempts: 0},
		},
	}
	gw := &spyGateway{}

	drainOnce(store, gw)

	if store.ackCount() != 1 {
		t.Errorf("want 1 Ack call, got %d", store.ackCount())
	}
	if store.incrCount() != 0 {
		t.Errorf("want 0 IncrementAttempts, got %d", store.incrCount())
	}
	if store.ackCalls[0] != 101 {
		t.Errorf("Ack ID: want 101, got %d", store.ackCalls[0])
	}
}

// TestDrainBatch_Failure verifies IncrementAttempts is called and Ack is not on dispatch failure.
func TestDrainBatch_Failure(t *testing.T) {
	payload, _ := json.Marshal(thebegateway.BlockRecord{BlockNumber: 2})
	store := &spyOutbox{
		nextEntries: []thebegateway.OutboxEntry{
			{ID: 202, Namespace: thebegateway.NamespaceBlock, Payload: payload, Attempts: 0},
		},
	}
	gw := &spyGateway{err: errDispatch}

	drainOnce(store, gw)

	if store.incrCount() != 1 {
		t.Errorf("want 1 IncrementAttempts, got %d", store.incrCount())
	}
	if store.ackCount() != 0 {
		t.Errorf("want 0 Ack calls, got %d", store.ackCount())
	}
	if store.incrCalls[0] != 202 {
		t.Errorf("IncrementAttempts ID: want 202, got %d", store.incrCalls[0])
	}
}

// TestDrainBatch_ExhaustedEntry verifies entries at MaxOutboxAttempts are skipped entirely
// — no dispatch, no Ack, no IncrementAttempts.
func TestDrainBatch_ExhaustedEntry(t *testing.T) {
	payload, _ := json.Marshal(thebegateway.BlockRecord{BlockNumber: 3})
	store := &spyOutbox{
		nextEntries: []thebegateway.OutboxEntry{
			{
				ID:        303,
				Namespace: thebegateway.NamespaceBlock,
				Payload:   payload,
				Attempts:  thebegateway.MaxOutboxAttempts,
			},
		},
	}
	gw := &spyGateway{}

	drainOnce(store, gw)

	if store.ackCount() != 0 {
		t.Errorf("exhausted entry: want 0 Ack, got %d", store.ackCount())
	}
	if store.incrCount() != 0 {
		t.Errorf("exhausted entry: want 0 IncrementAttempts, got %d", store.incrCount())
	}
	if gw.callCount() != 0 {
		t.Errorf("exhausted entry: gateway must not be called, got %d", gw.callCount())
	}
}

// TestDrainBatch_UnknownNamespace verifies unknown namespaces return nil from dispatch
// so the entry is Acked (drained) rather than retried forever.
func TestDrainBatch_UnknownNamespace(t *testing.T) {
	store := &spyOutbox{
		nextEntries: []thebegateway.OutboxEntry{
			{ID: 404, Namespace: "unknown_ns", Payload: []byte(`{}`), Attempts: 0},
		},
	}
	gw := &spyGateway{}

	drainOnce(store, gw)

	if store.ackCount() != 1 {
		t.Errorf("unknown namespace: want 1 Ack, got %d", store.ackCount())
	}
	if store.incrCount() != 0 {
		t.Errorf("unknown namespace: want 0 IncrementAttempts, got %d", store.incrCount())
	}
}

// TestDrainBatch_AllNamespaces table test — one entry per namespace present in dispatch switch.
// contract_receipt is excluded: the dispatch switch has no case for it (goes via 2PC on initial write).
func TestDrainBatch_AllNamespaces(t *testing.T) {
	accountPayload, _ := json.Marshal(thebegateway.AccountRecord{Address: "0xacc"})
	blockPayload, _ := json.Marshal(thebegateway.BlockRecord{BlockNumber: 10})
	snapshotPayload, _ := json.Marshal(thebegateway.SnapshotRecord{BlockNumber: 11})
	txPayload, _ := json.Marshal(thebegateway.TransactionRecord{TxHash: "0xtx"})
	zkPayload, _ := json.Marshal(thebegateway.ZKProofRecord{BlockNumber: 12})
	l1Payload, _ := json.Marshal(thebegateway.L1FinalityRecord{Confirmation: "0xfin"})

	cases := []struct {
		ns         thebegateway.Namespace
		payload    []byte
		wantMethod string
	}{
		{thebegateway.NamespaceAccount, accountPayload, "WriteAccount"},
		{thebegateway.NamespaceBlock, blockPayload, "WriteBlock"},
		{thebegateway.NamespaceSnapshot, snapshotPayload, "WriteSnapshot"},
		{thebegateway.NamespaceTransaction, txPayload, "WriteTransaction"},
		{thebegateway.NamespaceZKProof, zkPayload, "WriteZKProof"},
		{thebegateway.NamespaceL1Finality, l1Payload, "WriteL1Finality"},
	}

	for _, tc := range cases {
		t.Run(string(tc.ns), func(t *testing.T) {
			store := &spyOutbox{
				nextEntries: []thebegateway.OutboxEntry{
					{ID: 1, Namespace: tc.ns, Payload: tc.payload, Attempts: 0},
				},
			}
			gw := &spyGateway{}
			drainOnce(store, gw)

			if gw.callCount() != 1 {
				t.Errorf("%s: want 1 gateway call, got %d", tc.ns, gw.callCount())
			}
			if got := gw.lastMethod(); got != tc.wantMethod {
				t.Errorf("%s: want gateway method %q, got %q", tc.ns, tc.wantMethod, got)
			}
			if store.ackCount() != 1 {
				t.Errorf("%s: want 1 Ack, got %d", tc.ns, store.ackCount())
			}
		})
	}
}

// TestStop_SafeMultipleCalls verifies Stop() is idempotent (sync.Once guards channel close).
func TestStop_SafeMultipleCalls(t *testing.T) {
	w := thebegateway.NewOutboxWorker(&spyOutbox{}, &spyGateway{}, time.Hour)
	w.Start()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Stop() panicked: %v", r)
		}
	}()

	w.Stop()
	w.Stop()
}
