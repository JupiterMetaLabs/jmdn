package Block

import (
	"context"
	"net"
	"testing"

	commonv1 "gossipnode/proto/v1/common"
	mrev1 "gossipnode/proto/v1/mre"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ── bufconn harness ──────────────────────────────────────────────────────────
//
// These tests exercise the real mreRouter adapter against a fake MREService
// over an in-memory gRPC transport. They pin the adapter's response MAPPING
// (proto → domain), which the fakeRouter port tests deliberately do not
// cover. Staging parity remains the source of truth for server behavior;
// this is the client-side half of that contract.

// fakeMREServer returns canned responses; each test configures what it needs.
type fakeMREServer struct {
	mrev1.UnimplementedMREServiceServer

	submitResp  *commonv1.SubmitTxResponse
	pendingResp *commonv1.GetPendingResponse
	statsResp   *commonv1.GetMempoolStatsResponse
	feeResp     *commonv1.FeeStatistics

	gotSubmit  *commonv1.Transaction
	gotPending *commonv1.GetPendingRequest
}

func (f *fakeMREServer) SubmitTransaction(_ context.Context, tx *commonv1.Transaction) (*commonv1.SubmitTxResponse, error) {
	f.gotSubmit = tx
	return f.submitResp, nil
}

func (f *fakeMREServer) PeekPendingTransactions(_ context.Context, req *commonv1.GetPendingRequest) (*commonv1.GetPendingResponse, error) {
	f.gotPending = req
	return f.pendingResp, nil
}

func (f *fakeMREServer) GetMempoolStats(_ context.Context, _ *emptypb.Empty) (*commonv1.GetMempoolStatsResponse, error) {
	return f.statsResp, nil
}

func (f *fakeMREServer) GetFeeStatistics(_ context.Context, _ *emptypb.Empty) (*commonv1.FeeStatistics, error) {
	return f.feeResp, nil
}

// newBufconnRouter wires a real mreRouter to the fake server over bufconn.
func newBufconnRouter(t *testing.T, fake *fakeMREServer) *mreRouter {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	mrev1.RegisterMREServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &mreRouter{client: mrev1.NewMREServiceClient(conn), conn: conn}
}

// ── SubmitTransaction mapping ────────────────────────────────────────────────

func TestMreRouter_Submit_AcceptedMapsAllFields(t *testing.T) {
	fake := &fakeMREServer{submitResp: &commonv1.SubmitTxResponse{
		Success:         true,
		Hash:            "0xh",
		MempoolNode:     "shard-2",
		ReplicaMempools: []string{"shard-0", "shard-5"},
		TotalReplicas:   3,
	}}
	r := newBufconnRouter(t, fake)

	res, err := r.SubmitTransaction(context.Background(), fullTx(), "0xh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Accepted || res.Hash != "0xh" || res.PrimaryNode != "shard-2" || res.TotalNodes != 3 {
		t.Errorf("mapping wrong: %+v", res)
	}
	if len(res.ReplicaNodes) != 2 || res.ReplicaNodes[0] != "shard-0" {
		t.Errorf("replica nodes wrong: %v", res.ReplicaNodes)
	}
	// The wire tx must carry the converter's output (spot-check identity fields).
	if fake.gotSubmit.GetHash() != "0xh" || fake.gotSubmit.GetNonce() != 42 {
		t.Errorf("wire tx wrong: hash=%q nonce=%d", fake.gotSubmit.GetHash(), fake.gotSubmit.GetNonce())
	}
}

func TestMreRouter_Submit_RejectionIsNotAnError(t *testing.T) {
	fake := &fakeMREServer{submitResp: &commonv1.SubmitTxResponse{
		Success: false,
		Error:   "duplicate hash",
	}}
	r := newBufconnRouter(t, fake)

	res, err := r.SubmitTransaction(context.Background(), fullTx(), "0xh")
	if err != nil {
		t.Fatalf("rejection must not be a transport error, got %v", err)
	}
	if res.Accepted || res.RejectReason != "duplicate hash" {
		t.Errorf("rejection mapping wrong: %+v", res)
	}
}

// ── PeekPendingTransactions mapping ──────────────────────────────────────────

func TestMreRouter_Peek_MapsTransactionsAndMetadata(t *testing.T) {
	fake := &fakeMREServer{pendingResp: &commonv1.GetPendingResponse{
		Transactions: []*commonv1.Transaction{
			{Hash: "0xa", From: "0xF1", Nonce: 7},
			// Server-bug tolerance: a nil element does NOT survive the wire —
			// proto marshals it as an EMPTY message. The adapter must drop
			// hashless entries, which is what this pins.
			nil,
			{Hash: "0xb", From: "0xF2", Nonce: 8},
		},
		TotalFetched: 2,
		RoundsUsed:   1,
		DurationMs:   12,
	}}
	r := newBufconnRouter(t, fake)

	batch, err := r.PeekPendingTransactions(context.Background(), 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotPending.GetLimit() != 500 || fake.gotPending.GetFromReplica() {
		t.Errorf("request wrong: limit=%d fromReplica=%v", fake.gotPending.GetLimit(), fake.gotPending.GetFromReplica())
	}
	if len(batch.Transactions) != 2 {
		t.Fatalf("hashless (wire-decoded nil) entry must be skipped: got %d txs", len(batch.Transactions))
	}
	if batch.Transactions[0].GetHash() != "0xa" || batch.Transactions[1].GetNonce() != 8 {
		t.Errorf("tx view wrong: %v %v", batch.Transactions[0].GetHash(), batch.Transactions[1].GetNonce())
	}
	if batch.TotalFetched != 2 || batch.RoundsUsed != 1 || batch.DurationMs != 12 {
		t.Errorf("metadata wrong: %+v", batch)
	}
}

// ── GetMempoolStats remap ────────────────────────────────────────────────────
//
// Pins the legacy-parity remap (tracker §2): QueueCount←total_cache_size,
// DbCount←total_primary_txns.

func TestMreRouter_Stats_LegacyParityRemap(t *testing.T) {
	fake := &fakeMREServer{statsResp: &commonv1.GetMempoolStatsResponse{
		Aggregated: &commonv1.AggregatedStats{
			TotalCacheSize:   17,
			TotalPrimaryTxns: 240,
		},
		NodeCount:    3,
		HealthyNodes: 2,
	}}
	r := newBufconnRouter(t, fake)

	s, err := r.GetMempoolStats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.QueueCount != 17 || s.DbCount != 240 || s.NodeCount != 3 || s.HealthyNodes != 2 {
		t.Errorf("remap wrong: %+v", s)
	}
}

func TestMreRouter_Stats_NilAggregatedIsZero(t *testing.T) {
	fake := &fakeMREServer{statsResp: &commonv1.GetMempoolStatsResponse{NodeCount: 1}}
	r := newBufconnRouter(t, fake)

	s, err := r.GetMempoolStats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.QueueCount != 0 || s.DbCount != 0 {
		t.Errorf("nil aggregated must map to zeros, got %+v", s)
	}
}

// ── GetFeeStatistics mapping ─────────────────────────────────────────────────

func TestMreRouter_Fees_MapsValuesAndRecommended(t *testing.T) {
	fake := &fakeMREServer{feeResp: &commonv1.FeeStatistics{
		MinFee:    1,
		MaxFee:    4,
		MedianFee: 2,
		MeanFee:   3,
		RecommendedFees: &commonv1.RecommendedFees{
			Slow: 10, Standard: 20, Fast: 30, Instant: 40,
		},
	}}
	r := newBufconnRouter(t, fake)

	f, err := r.GetFeeStatistics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.MinFee != 1 || f.MaxFee != 4 || f.MedianFee != 2 || f.MeanFee != 3 {
		t.Errorf("fee values wrong: %+v", f)
	}
	if f.Recommended.Standard != 20 || f.Recommended.Instant != 40 {
		t.Errorf("recommended wrong: %+v", f.Recommended)
	}
}

func TestMreRouter_Fees_NilRecommendedIsZero(t *testing.T) {
	fake := &fakeMREServer{feeResp: &commonv1.FeeStatistics{MedianFee: 2}}
	r := newBufconnRouter(t, fake)

	f, err := r.GetFeeStatistics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Recommended != (RecommendedFees{}) {
		t.Errorf("nil recommended must map to zero struct, got %+v", f.Recommended)
	}
}
