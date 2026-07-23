package Block

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gossipnode/config"
)

// fakeRouter is a hand-rolled MempoolRouter double. It exists to test the
// package-level facades (SubmitToMempool, GetFeeStatisticsFromRouting) and to
// demonstrate the injection seam consumers' tests should use.
type fakeRouter struct {
	submitResult *SubmitResult
	submitErr    error
	feeStats     *FeeStats
	feeErr       error

	gotHash string
}

var _ MempoolRouter = (*fakeRouter)(nil)

func (f *fakeRouter) SubmitTransaction(_ context.Context, _ *config.Transaction, txHash string) (*SubmitResult, error) {
	f.gotHash = txHash
	return f.submitResult, f.submitErr
}

func (f *fakeRouter) PeekPendingTransactions(_ context.Context, _ int32) (*PendingBatch, error) {
	return &PendingBatch{}, nil
}

func (f *fakeRouter) GetMempoolStats(_ context.Context) (*MempoolStatsSummary, error) {
	return &MempoolStatsSummary{}, nil
}

func (f *fakeRouter) GetFeeStatistics(_ context.Context) (*FeeStats, error) {
	return f.feeStats, f.feeErr
}

// withRouter installs a fake router for the duration of a test and restores
// the previous singleton afterwards (tests must not leak global state).
func withRouter(t *testing.T, r MempoolRouter) {
	t.Helper()
	prev := routingClient
	SetRoutingClient(r)
	t.Cleanup(func() { SetRoutingClient(prev) })
}

// ── SubmitToMempool: the facade's error contract ─────────────────────────────
//
// The port distinguishes transport failure (error) from MRE rejection
// (Accepted=false). The facade collapses both into an error for its async
// caller — these tests pin that both paths actually surface, since the old
// client conflated them and rejections were unclassifiable.

func TestSubmitToMempool_Accepted(t *testing.T) {
	fake := &fakeRouter{submitResult: &SubmitResult{Accepted: true, Hash: "0xh"}}
	withRouter(t, fake)

	if err := SubmitToMempool(context.Background(), &config.Transaction{}, "0xh"); err != nil {
		t.Fatalf("accepted submit must return nil, got %v", err)
	}
	if fake.gotHash != "0xh" {
		t.Errorf("router received hash %q, want 0xh", fake.gotHash)
	}
}

func TestSubmitToMempool_RejectionSurfacesReason(t *testing.T) {
	fake := &fakeRouter{submitResult: &SubmitResult{Accepted: false, RejectReason: "duplicate hash"}}
	withRouter(t, fake)

	err := SubmitToMempool(context.Background(), &config.Transaction{}, "0xh")
	if err == nil {
		t.Fatal("rejected submit must return an error")
	}
	if !strings.Contains(err.Error(), "duplicate hash") {
		t.Errorf("rejection reason must survive into the error, got %q", err.Error())
	}
}

func TestSubmitToMempool_TransportErrorPassesThrough(t *testing.T) {
	transportErr := errors.New("connection refused")
	fake := &fakeRouter{submitErr: transportErr}
	withRouter(t, fake)

	err := SubmitToMempool(context.Background(), &config.Transaction{}, "0xh")
	if !errors.Is(err, transportErr) {
		t.Errorf("transport error must pass through unwrapped-able, got %v", err)
	}
}

func TestSubmitToMempool_NoRouterInitialized(t *testing.T) {
	withRouter(t, nil) // simulate pre-init state; restored by cleanup

	err := SubmitToMempool(context.Background(), &config.Transaction{}, "0xh")
	if err == nil {
		t.Fatal("must error when routing client is not initialized")
	}
}

// ── GetFeeStatisticsFromRouting ──────────────────────────────────────────────

func TestGetFeeStatisticsFromRouting_Passthrough(t *testing.T) {
	want := &FeeStats{MedianFee: 35_000_000_000, Recommended: RecommendedFees{Standard: 35_000_000_000}}
	withRouter(t, &fakeRouter{feeStats: want})

	got, err := GetFeeStatisticsFromRouting(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MedianFee != want.MedianFee || got.Recommended.Standard != want.Recommended.Standard {
		t.Errorf("fee stats not passed through: got %+v", got)
	}
}

func TestGetFeeStatisticsFromRouting_ErrorPassesThrough(t *testing.T) {
	feeErr := errors.New("mre unavailable")
	withRouter(t, &fakeRouter{feeErr: feeErr})

	if _, err := GetFeeStatisticsFromRouting(context.Background()); !errors.Is(err, feeErr) {
		t.Errorf("fee error must pass through, got %v", err)
	}
}
