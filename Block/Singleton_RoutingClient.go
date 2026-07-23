package Block

import (
	"context"
	"fmt"
	"time"

	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"
	commonv1 "gossipnode/proto/v1/common"
	mrev1 "gossipnode/proto/v1/mre"

	"github.com/JupiterMetaLabs/ion"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// mreRouter is the concrete MempoolRouter speaking the MRE v1 gRPC API
// (/jmdt.proto.mre.v1.MREService). It owns the connection and the generated
// client; no generated types escape this file's methods.
type mreRouter struct {
	client mrev1.MREServiceClient
	conn   *grpc.ClientConn
}

// compile-time port conformance
var _ MempoolRouter = (*mreRouter)(nil)

// routingClient is the process-wide singleton. Lifecycle: created once at
// startup by NewRoutingServiceClient, read via GetRoutingClient, replaced in
// tests via SetRoutingClient.
var routingClient MempoolRouter

const routingCallTimeout = 5 * time.Second

// NewRoutingServiceClient dials the MRE and installs the singleton router.
// Reuses the existing singleton if already initialized.
func NewRoutingServiceClient(loggerCtx context.Context, address string) (MempoolRouter, error) {
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(loggerCtx, "RoutingClient.NewRoutingServiceClient")
	defer span.End()
	span.SetAttributes(attribute.String("address", address))

	if routingClient != nil {
		span.SetAttributes(attribute.String("status", "reused_singleton"))
		return routingClient, nil
	}

	secCfg := &settings.Get().Security
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger().NamedLogger)

	// We identify as "mempool_client" connecting to ServiceMempool
	// (routing shares the mempool endpoint's TLS identity).
	creds, err := tlsLoader.LoadClientCredentials(settings.ServiceMempool, "mempool_client")
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "tls_config_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to load TLS credentials for Routing Client", err,
			ion.String("address", address))
		return nil, fmt.Errorf("failed to load TLS credentials for Routing Client: %w", err)
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to connect to Routing Service", err,
			ion.String("address", address),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.NewRoutingServiceClient"))
		return nil, fmt.Errorf("failed to connect to Routing Service: %w", err)
	}

	routingClient = &mreRouter{
		client: mrev1.NewMREServiceClient(conn),
		conn:   conn,
	}

	span.SetAttributes(attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Routing client initialized (MRE v1)",
		ion.String("address", address),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.NewRoutingServiceClient"))

	return routingClient, nil
}

// GetRoutingClient returns the singleton router.
//
// Deliberately does NOT log: a nil singleton is an initialization-order bug
// the caller must handle, and pulling the global logging stack (which
// requires loaded settings) into this accessor couples a trivial lookup to
// process-wide config — callers log with their own context.
func GetRoutingClient(_ context.Context) (MempoolRouter, error) {
	if routingClient == nil {
		return nil, fmt.Errorf("routing client is nil (not initialized — call NewRoutingServiceClient at startup)")
	}
	return routingClient, nil
}

// SetRoutingClient replaces the singleton (tests / manual override).
func SetRoutingClient(client MempoolRouter) {
	routingClient = client
}

// CloseRoutingClient tears down the singleton's connection (shutdown path).
func CloseRoutingClient() {
	if r, ok := routingClient.(*mreRouter); ok && r.conn != nil {
		_ = r.conn.Close()
	}
	routingClient = nil
}

// ── MempoolRouter implementation ─────────────────────────────────────────────

// SubmitTransaction routes one signed transaction through the MRE.
// Transport failure → (nil, err). MRE rejection → (&SubmitResult{Accepted:
// false, RejectReason: ...}, nil).
func (r *mreRouter) SubmitTransaction(loggerCtx context.Context, tx *config.Transaction, txHash string) (*SubmitResult, error) {
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(loggerCtx, "RoutingClient.SubmitTransaction")
	defer span.End()

	ctx, cancel := context.WithTimeout(spanCtx, 10*time.Second)
	defer cancel()

	span.SetAttributes(
		attribute.String("tx_hash", txHash),
		attribute.Int64("nonce", int64(tx.Nonce)),
		attribute.Int("tx_type", int(tx.Type)),
	)

	resp, err := r.client.SubmitTransaction(ctx, convertToPbTransaction(tx, txHash))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "submit_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to submit transaction to MRE", err,
			ion.String("tx_hash", txHash),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.SubmitTransaction"))
		return nil, fmt.Errorf("failed to submit transaction to mempool: %w", err)
	}

	result := &SubmitResult{
		Accepted:     resp.GetSuccess(),
		Hash:         resp.GetHash(),
		RejectReason: resp.GetError(),
		PrimaryNode:  resp.GetMempoolNode(),
		ReplicaNodes: resp.GetReplicaMempools(),
		TotalNodes:   resp.GetTotalReplicas(),
	}

	span.SetAttributes(
		attribute.Bool("response_success", result.Accepted),
		attribute.String("mempool_node", result.PrimaryNode),
		attribute.Int("total_nodes", int(result.TotalNodes)),
	)

	if !result.Accepted {
		span.SetAttributes(attribute.String("status", "rejected"), attribute.String("rejection_error", result.RejectReason))
		logger().NamedLogger.Error(spanCtx, "MRE rejected transaction",
			fmt.Errorf("mempool rejected transaction: %s", result.RejectReason),
			ion.String("tx_hash", txHash),
			ion.String("error", result.RejectReason),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.SubmitTransaction"))
		return result, nil
	}

	span.SetAttributes(attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Transaction routed to mempool",
		ion.String("tx_hash", result.Hash),
		ion.String("primary_node", result.PrimaryNode),
		ion.Int("total_nodes", int(result.TotalNodes)),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.SubmitTransaction"))

	return result, nil
}

// PeekPendingTransactions performs a non-destructive read of pending
// transactions via the typed v1 stub (replaces the former raw conn.Invoke
// that decoded into a legacy type on accidental wire compatibility).
func (r *mreRouter) PeekPendingTransactions(loggerCtx context.Context, limit int32) (*PendingBatch, error) {
	ctx, cancel := context.WithTimeout(loggerCtx, routingCallTimeout)
	defer cancel()

	resp, err := r.client.PeekPendingTransactions(ctx, &commonv1.GetPendingRequest{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("PeekPendingTransactions: %w", err)
	}

	// Skip unusable entries. Note: nil elements in a repeated proto field do
	// not survive the wire — the marshaller encodes them as EMPTY messages —
	// so the effective server-bug guard is the empty-hash check, not the nil
	// check (which only covers direct in-process use).
	txs := make([]PendingTx, 0, len(resp.GetTransactions()))
	for _, tx := range resp.GetTransactions() {
		if tx != nil && tx.GetHash() != "" {
			txs = append(txs, tx)
		}
	}

	return &PendingBatch{
		Transactions: txs,
		TotalFetched: resp.GetTotalFetched(),
		RoundsUsed:   resp.GetRoundsUsed(),
		DurationMs:   resp.GetDurationMs(),
	}, nil
}

// GetMempoolStats returns fleet-aggregated counters. Field mapping documented
// on MempoolStatsSummary.
func (r *mreRouter) GetMempoolStats(loggerCtx context.Context) (*MempoolStatsSummary, error) {
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(loggerCtx, "RoutingClient.GetMempoolStats")
	defer span.End()

	ctx, cancel := context.WithTimeout(spanCtx, routingCallTimeout)
	defer cancel()

	resp, err := r.client.GetMempoolStats(ctx, &emptypb.Empty{})
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to get mempool stats", err,
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.GetMempoolStats"))
		return nil, err
	}

	agg := resp.GetAggregated()
	summary := &MempoolStatsSummary{
		QueueCount:   agg.GetTotalCacheSize(),
		DbCount:      agg.GetTotalPrimaryTxns(),
		NodeCount:    resp.GetNodeCount(),
		HealthyNodes: resp.GetHealthyNodes(),
	}

	span.SetAttributes(
		attribute.Int64("db_count", summary.DbCount),
		attribute.Int("healthy_nodes", int(summary.HealthyNodes)),
		attribute.String("status", "success"),
	)
	return summary, nil
}

// GetFeeStatistics returns the MRE fee view (upstream stub — see tracker U-1).
func (r *mreRouter) GetFeeStatistics(loggerCtx context.Context) (*FeeStats, error) {
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(loggerCtx, "RoutingClient.GetFeeStatistics")
	defer span.End()

	ctx, cancel := context.WithTimeout(spanCtx, routingCallTimeout)
	defer cancel()

	resp, err := r.client.GetFeeStatistics(ctx, &emptypb.Empty{})
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to get fee statistics", err,
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.GetFeeStatistics"))
		return nil, err
	}

	stats := &FeeStats{
		MinFee:    resp.GetMinFee(),
		MaxFee:    resp.GetMaxFee(),
		MedianFee: resp.GetMedianFee(),
		MeanFee:   resp.GetMeanFee(),
	}
	if rec := resp.GetRecommendedFees(); rec != nil {
		stats.Recommended = RecommendedFees{
			Slow:     rec.GetSlow(),
			Standard: rec.GetStandard(),
			Fast:     rec.GetFast(),
			Instant:  rec.GetInstant(),
		}
	}

	span.SetAttributes(
		attribute.Int64("median_fee", int64(stats.MedianFee)),
		attribute.String("status", "success"),
	)
	return stats, nil
}
