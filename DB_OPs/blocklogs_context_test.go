package DB_OPs

// Review finding PP-4de521. eth_getLogs is the heaviest RPC element and it used
// to build its own deadline from context.Background(), so the caller's context
// reached nothing. The batch dispatcher stopped STARTING work at its 25s
// deadline, but a scan already running kept going for its full 10s — a batch
// could reach ~35s against a 30s WriteTimeout, and the client lost the response
// it had already paid for. A client disconnect did not stop a scan either.

import (
	"context"
	"testing"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/gETH/Facade/Service/Types"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// blockingLogHandle stands in for a scan that would run for the full
// logQueryTimeout: it returns only when its context ends.
type blockingLogHandle struct {
	store.ThebeHandle
	started chan struct{}
}

func (h *blockingLogHandle) GetLogs(ctx context.Context, _ store.LogFilter) ([]*ethtypes.Log, error) {
	select {
	case <-h.started:
	default:
		close(h.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func withBlockingLogHandle(t *testing.T) *blockingLogHandle {
	t.Helper()
	h := &blockingLogHandle{started: make(chan struct{})}
	globalThebeHandleMu.RLock()
	prev := globalThebeHandle
	globalThebeHandleMu.RUnlock()
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(prev) })
	return h
}

// A cancelled caller stops the scan instead of leaving it to run out its own
// 10s ceiling.
func TestGetLogs_CallerCancellationStopsTheScan(t *testing.T) {
	h := withBlockingLogHandle(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_, _ = GetLogs(ctx, nil, Types.FilterQuery{FromBlock: nil, ToBlock: nil})
		done <- time.Since(start)
	}()

	<-h.started // the scan is running
	cancel()

	select {
	case elapsed := <-done:
		// Generous bound: the point is that it does NOT wait out logQueryTimeout.
		if elapsed > logQueryTimeout/2 {
			t.Fatalf("GetLogs took %v after cancellation; it must not run out its %v ceiling",
				elapsed, logQueryTimeout)
		}
	case <-time.After(logQueryTimeout / 2):
		t.Fatalf("GetLogs ignored the caller's cancellation")
	}
}

// A caller whose deadline is shorter than logQueryTimeout gets the shorter one:
// the ceiling is a cap on top of the caller's context, not a replacement.
func TestGetLogs_CallerDeadlineWinsWhenShorter(t *testing.T) {
	h := withBlockingLogHandle(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := GetLogs(ctx, nil, Types.FilterQuery{})
	elapsed := time.Since(start)

	<-h.started
	if err == nil {
		t.Fatal("want a deadline error")
	}
	if elapsed > logQueryTimeout/2 {
		t.Fatalf("GetLogs honoured its own %v ceiling over the caller's 100ms (took %v)",
			logQueryTimeout, elapsed)
	}
}

// A caller with no context of its own is unchanged: the ceiling still applies
// and nothing panics on a nil context.
func TestGetLogs_NilContextIsAccepted(t *testing.T) {
	h := withBlockingLogHandle(t)

	done := make(chan struct{})
	go func() {
		//nolint:staticcheck // passing nil is exactly what this test covers
		_, _ = GetLogs(nil, nil, Types.FilterQuery{})
		close(done)
	}()

	select {
	case <-h.started: // reached the scan without panicking
	case <-done:
		t.Fatal("GetLogs returned before reaching the store")
	case <-time.After(2 * time.Second):
		t.Fatal("GetLogs with a nil context never reached the store")
	}
}
