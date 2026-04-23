package Block

import (
	"context"
	"fmt"
	"math/big"
	"time"

	pb "gossipnode/Mempool/proto"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"

	// "github.com/golang/protobuf/ptypes/empty"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
)

const (
	FILENAME   = ""
	TOPIC      = "mempool"
	BLOCKTOPIC = "block"
	KEEP_LOGS  = true
	BATCH_SIZE = 100
	BATCH_WAIT = 10 * time.Second
	TIMEOUT    = 5 * time.Second
)

// MempoolClient provides methods to interact with the mempool service
type MempoolClient struct {
	client pb.MempoolServiceClient
	conn   *grpc.ClientConn
}

// NewMempoolClient creates a new mempool client connection
func NewMempoolClient(logger_ctx context.Context, address string) (*MempoolClient, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.NewMempoolClient")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("address", address))

	logger().NamedLogger.Info(spanCtx, "Creating new mempool client",
		ion.String("address", address),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.NewMempoolClient"))

	// 1. Setup TLS Loader
	secCfg := &settings.Get().Security
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger().NamedLogger)

	// 2. Load Client Credentials (Standardized Helper)
	creds, err := tlsLoader.LoadClientCredentials(settings.ServiceMempool, "mempool_client")
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "tls_config_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to load TLS credentials for Mempool Client", err,
			ion.String("address", address))
		return nil, fmt.Errorf("failed to load TLS credentials for Mempool Client: %w", err)
	}

	logger().NamedLogger.Info(spanCtx, "Mempool Client Credentials Loaded", ion.String("address", address))

	// Create a gRPC connection to the mempool service
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to connect to mempool service",
			err,
			ion.String("address", address),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.NewMempoolClient"))
		return nil, fmt.Errorf("failed to connect to mempool service: %v", err)
	}

	client := pb.NewMempoolServiceClient(conn)

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully created mempool client",
		ion.String("address", address),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.NewMempoolClient"))

	return &MempoolClient{
		client: client,
		conn:   conn,
	}, nil
}

// Close closes the gRPC connection
func (m *MempoolClient) Close() error {

	// Close the gRPC connection
	if m.conn != nil {
		return m.conn.Close()
	}
	return nil
}

// SubmitTransaction submits a transaction to the mempool
func (m *MempoolClient) SubmitTransaction(logger_ctx context.Context, tx *config.Transaction, txHash string) error {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.SubmitTransaction")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 10*time.Second)
	logger().NamedLogger.Info(spanCtx, "Submitting transaction with timeout", ion.String("timeout", "10s"))
	defer cancel()

	// Retrieve routing client (single call)
	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get routing client", err,
			ion.String("tx_hash", txHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.SubmitTransaction"))
		return fmt.Errorf("routing client connection failed: %v", err)
	}
	logger().NamedLogger.Info(spanCtx, "Successfully obtained routing client", ion.String("address", RoutingClient.conn.Target()))

	span.SetAttributes(
		attribute.String("tx_hash", txHash),
		attribute.String("from", tx.From.Hex()),
		attribute.String("to", tx.To.Hex()),
		attribute.Int64("nonce", int64(tx.Nonce)),
		attribute.Int("tx_type", int(tx.Type)),
	)

	logger().NamedLogger.Info(spanCtx, "Submitting transaction to mempool",
		ion.String("tx_hash", txHash),
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()),
		ion.Int64("nonce", int64(tx.Nonce)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransaction"))

	// Convert the transaction to the protobuf format
	pbTx := convertToPbTransaction(tx, txHash)

	logger().NamedLogger.Info(spanCtx, "Calling SubmitTransaction on routing client",
		ion.String("tx_hash", txHash),
		ion.String("timeout", "10s"),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransaction"))

	grpcStart := time.Now().UTC()
	resp, err := RoutingClient.client.SubmitTransaction(ctx, pbTx)
	grpcDuration := time.Since(grpcStart).Seconds()
	span.SetAttributes(attribute.Float64("grpc_duration", grpcDuration))

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "submit_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to submit transaction to mempool",
			err,
			ion.String("tx_hash", txHash),
			ion.Float64("grpc_duration", grpcDuration),
			ion.Float64("total_duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.SubmitTransaction"))
		return fmt.Errorf("failed to submit transaction to mempool: %v", err)
	}

	span.SetAttributes(
		attribute.Bool("response_success", resp.Success),
		attribute.String("response_hash", resp.Hash),
		attribute.String("mempool_node", resp.MempoolNode),
		attribute.Int("total_replicas", int(resp.TotalReplicas)),
		attribute.Int("replica_mempools_count", len(resp.ReplicaMempools)),
	)

	logger().NamedLogger.Info(spanCtx, "SubmitTransaction call completed successfully",
		ion.String("tx_hash", txHash),
		ion.Bool("success", resp.Success),
		ion.String("response_hash", resp.Hash),
		ion.Float64("grpc_duration", grpcDuration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransaction"))

	if len(resp.ReplicaMempools) > 0 {
		logger().NamedLogger.Info(spanCtx, "Replica mempools",
			ion.Int("replica_count", len(resp.ReplicaMempools)),
			ion.String("replica_mempools", fmt.Sprintf("%v", resp.ReplicaMempools)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.SubmitTransaction"))
	}

	if !resp.Success {
		span.RecordError(fmt.Errorf("mempool rejected transaction: %s", resp.Error))
		span.SetAttributes(attribute.String("status", "rejected"), attribute.String("rejection_error", resp.Error))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Mempool rejected transaction",
			fmt.Errorf("mempool rejected transaction: %s", resp.Error),
			ion.String("tx_hash", resp.Hash),
			ion.String("error", resp.Error),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.SubmitTransaction"))
		return fmt.Errorf("mempool rejected transaction: %s", resp.Error)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Transaction successfully submitted to mempool",
		ion.String("tx_hash", resp.Hash),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransaction"))

	return nil
}

// SubmitTransactions submits a batch of transactions to the mempool
func (m *MempoolClient) SubmitTransactions(logger_ctx context.Context, txs []*config.Transaction) (*pb.BatchSubmitResponse, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.SubmitTransactions")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 15*time.Second) // Longer timeout for batches
	defer cancel()

	span.SetAttributes(attribute.Int("batch_size", len(txs)))

	logger().NamedLogger.Info(spanCtx, "Submitting batch of transactions to mempool",
		ion.Int("batch_size", len(txs)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransactions"))

	pbTxs := make([]*pb.Transaction, len(txs))
	for i, tx := range txs {
		// The ZKBlockTransaction should have a pre-computed hash.
		if tx.Hash == (common.Hash{}) {
			span.RecordError(fmt.Errorf("transaction at index %d has no hash", i))
			span.SetAttributes(attribute.String("status", "invalid_tx"), attribute.Int("invalid_tx_index", i))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return nil, fmt.Errorf("transaction at index %d has no hash", i)
		}
		pbTxs[i] = convertToPbTransaction(tx, tx.Hash.Hex())
	}

	batch := &pb.TransactionBatch{
		Transactions: pbTxs,
	}

	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, err
	}

	logger().NamedLogger.Info(spanCtx, "Calling SubmitTransactions on routing client",
		ion.Int("batch_size", len(txs)),
		ion.String("timeout", "15s"),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransactions"))

	grpcStart := time.Now().UTC()
	resp, err := RoutingClient.client.SubmitTransactions(ctx, batch)
	grpcDuration := time.Since(grpcStart).Seconds()
	span.SetAttributes(attribute.Float64("grpc_duration", grpcDuration))

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "submit_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to submit transactions to mempool",
			err,
			ion.Int("batch_size", len(txs)),
			ion.Float64("grpc_duration", grpcDuration),
			ion.Float64("total_duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.SubmitTransactions"))
		return nil, fmt.Errorf("routing client could not submit transactions: %s", err)
	}

	span.SetAttributes(
		attribute.Bool("response_success", resp.Success),
		attribute.Int("response_count", int(resp.Count)),
	)

	logger().NamedLogger.Info(spanCtx, "SubmitTransactions call completed successfully",
		ion.Int("batch_size", len(txs)),
		ion.Bool("success", resp.Success),
		ion.Int("response_count", int(resp.Count)),
		ion.Float64("grpc_duration", grpcDuration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransactions"))

	if !resp.Success {
		span.RecordError(fmt.Errorf("mempool rejected transaction batch: %s", resp.Error))
		span.SetAttributes(attribute.String("status", "rejected"), attribute.String("rejection_error", resp.Error))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		// The response itself is returned to allow the caller to inspect partial successes if applicable.
		return resp, fmt.Errorf("mempool rejected transaction batch: %s", resp.Error)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Transactions successfully submitted to mempool",
		ion.Int("count", int(resp.Count)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.SubmitTransactions"))

	return resp, nil
}

// GetTransaction retrieves a specific transaction from the mempool by its hash
func (m *MempoolClient) GetTransaction(logger_ctx context.Context, hash string) (*pb.Transaction, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.GetTransaction")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	span.SetAttributes(attribute.String("tx_hash", hash))

	logger().NamedLogger.Info(spanCtx, "Getting transaction from mempool",
		ion.String("tx_hash", hash),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetTransaction"))

	req := &pb.GetTransactionRequest{Hash: hash}
	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, err
	}

	tx, err := RoutingClient.client.GetTransaction(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get transaction from mempool",
			err,
			ion.String("tx_hash", hash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.GetTransaction"))
		return nil, fmt.Errorf("failed to get transaction %s: %v", hash, err)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved transaction from mempool",
		ion.String("tx_hash", hash),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetTransaction"))

	return tx, nil
}

// GetPendingTransactions retrieves a list of pending transactions from the mempool
func (m *MempoolClient) GetPendingTransactions(logger_ctx context.Context, limit int32) (*pb.TransactionBatch, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.GetPendingTransactions")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	span.SetAttributes(attribute.Int64("limit", int64(limit)))

	logger().NamedLogger.Info(spanCtx, "Getting pending transactions from mempool",
		ion.Int64("limit", int64(limit)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetPendingTransactions"))

	req := &pb.GetPendingRequest{Limit: limit}
	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, err
	}

	batch, err := RoutingClient.client.GetPendingTransactions(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get pending transactions from mempool",
			err,
			ion.Int64("limit", int64(limit)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.GetPendingTransactions"))
		return nil, fmt.Errorf("failed to get pending transactions: %v", err)
	}

	span.SetAttributes(attribute.Int("transactions_count", len(batch.Transactions)))

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved pending transactions from mempool",
		ion.Int64("limit", int64(limit)),
		ion.Int("transactions_count", len(batch.Transactions)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetPendingTransactions"))

	return batch, nil
}

// GetMempoolStats gets the current mempool statistics
func (m *MempoolClient) GetMempoolStats(logger_ctx context.Context) (*pb.MREStats, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.GetMempoolStats")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	logger().NamedLogger.Info(spanCtx, "Getting mempool statistics",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetMempoolStats"))

	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to get routing client: %v", err)
	}

	// Use the empty.Empty type directly
	stats, err := RoutingClient.GetMempoolStats(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get mempool stats",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.GetMempoolStats"))
		return nil, fmt.Errorf("failed to get mempool stats: %v", err)
	}

	// Note: MREStats fields may vary - only set attributes if fields exist
	if stats != nil {
		// Set basic attributes that are likely to exist
		span.SetAttributes(attribute.String("stats_retrieved", "true"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved mempool statistics",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetMempoolStats"))

	return stats, nil
}

type GasFeeStats struct {
	MaxFee          uint64
	MinFee          uint64
	MedianFee       uint64
	MeanFee         uint64
	RecommendedFees *pb.RecommendedFees
}

// GetFeeStatisticsFromRouting gets fee statistics directly from routing service
// This is the recommended way to access routing service functionality
func GetFeeStatisticsFromRouting(logger_ctx context.Context) (*GasFeeStats, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.GetFeeStatisticsFromRouting")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	logger().NamedLogger.Info(spanCtx, "Getting fee statistics from routing service",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetFeeStatisticsFromRouting"))

	routingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to get routing client: %v", err)
	}

	stats, err := routingClient.GetFeeStatistics(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get fee statistics",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.GetFeeStatisticsFromRouting"))
		return nil, fmt.Errorf("failed to get fee statistics: %v", err)
	}

	if stats != nil {
		span.SetAttributes(
			attribute.Int64("max_fee", int64(stats.MaxFee)),
			attribute.Int64("min_fee", int64(stats.MinFee)),
			attribute.Int64("median_fee", int64(stats.MedianFee)),
			attribute.Int64("mean_fee", int64(stats.MeanFee)),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved fee statistics from routing service",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetFeeStatisticsFromRouting"))

	return &GasFeeStats{
		MaxFee:          stats.MaxFee,
		MinFee:          stats.MinFee,
		MedianFee:       stats.MedianFee,
		MeanFee:         stats.MeanFee,
		RecommendedFees: stats.RecommendedFees,
	}, nil
}

// GetFeeStatistics gets detailed fee statistics from the mempool
func (m *MempoolClient) GetFeeStatistics(logger_ctx context.Context) (*pb.FeeStatistics, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.GetFeeStatistics")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	logger().NamedLogger.Info(spanCtx, "Getting fee statistics from mempool",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetFeeStatistics"))

	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to get routing client: %v", err)
	}

	stats, err := RoutingClient.GetFeeStatistics(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get fee statistics",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.GetFeeStatistics"))
		return nil, fmt.Errorf("failed to get fee statistics: %v", err)
	}

	if stats != nil {
		span.SetAttributes(
			attribute.Int64("max_fee", int64(stats.MaxFee)),
			attribute.Int64("min_fee", int64(stats.MinFee)),
			attribute.Int64("median_fee", int64(stats.MedianFee)),
			attribute.Int64("mean_fee", int64(stats.MeanFee)),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved fee statistics from mempool",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.GetFeeStatistics"))

	return stats, nil
}

// Wrapper function for getting FeeStatistics from mempool service
func (m *MempoolClient) WrapperGetFeeStatistics(logger_ctx context.Context) (*GasFeeStats, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.WrapperGetFeeStatistics")
	defer span.End()

	startTime := time.Now().UTC()
	ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
	defer cancel()

	logger().NamedLogger.Info(spanCtx, "Getting fee statistics (wrapper) from mempool",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.WrapperGetFeeStatistics"))

	RoutingClient, err := GetRoutingClient(logger_ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "routing_client_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to get routing client: %v", err)
	}

	stats, err := RoutingClient.GetFeeStatistics(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get fee statistics",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "gRPCClient.WrapperGetFeeStatistics"))
		return nil, fmt.Errorf("failed to get fee statistics: %v", err)
	}

	if stats != nil {
		span.SetAttributes(
			attribute.Int64("max_fee", int64(stats.MaxFee)),
			attribute.Int64("min_fee", int64(stats.MinFee)),
			attribute.Int64("median_fee", int64(stats.MedianFee)),
			attribute.Int64("mean_fee", int64(stats.MeanFee)),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Successfully retrieved fee statistics (wrapper) from mempool",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.WrapperGetFeeStatistics"))

	return &GasFeeStats{
		MaxFee:          stats.MaxFee,
		MinFee:          stats.MinFee,
		MedianFee:       stats.MedianFee,
		MeanFee:         stats.MeanFee,
		RecommendedFees: stats.RecommendedFees,
	}, nil
}

// Helper function to convert a config.Transaction to pb.Transaction
func convertToPbTransaction(tx *config.Transaction, txHash string) *pb.Transaction {
	// Helper function to safely convert big.Int to decimal string
	getBigIntString := func(b *big.Int) string {
		if b == nil {
			return "0"
		}
		return b.String()
	}

	// Helper function to safely convert signature fields
	getSignatureString := func(b *big.Int) string {
		if b == nil || b.Cmp(big.NewInt(0)) == 0 {
			return "0x0"
		}
		return "0x" + b.Text(16)
	}

	// Helper function to safely convert data field
	getDataBytes := func(data []byte) []byte {
		if data == nil {
			return []byte{}
		}
		return data
	}

	// Helper function to safely convert address to string
	addrToString := func(addr *common.Address) string {
		if addr == nil {
			return ""
		}
		return addr.Hex()
	}

	pbTx := &pb.Transaction{
		Hash:           txHash,
		From:           addrToString(tx.From),
		To:             addrToString(tx.To),
		Value:          getBigIntString(tx.Value),
		Type:           0, // Will be set correctly below
		Timestamp:      uint64(tx.Timestamp),
		ChainId:        getBigIntString(tx.ChainID),
		Nonce:          uint64(tx.Nonce),
		GasLimit:       fmt.Sprintf("%d", tx.GasLimit),
		GasPrice:       getBigIntString(tx.GasPrice),
		MaxFee:         getBigIntString(tx.MaxFee),
		MaxPriorityFee: getBigIntString(tx.MaxPriorityFee),
		Data:           getDataBytes(tx.Data),
		AccessList:     convertAccessListToPb(tx.AccessList),
		V:              getSignatureString(tx.V),
		R:              getSignatureString(tx.R),
		S:              getSignatureString(tx.S),
	}

	if tx.Timestamp != 0 {
		// Assuming tx.Timestamp is already a Unix timestamp (uint64)
		pbTx.Timestamp = uint64(tx.Timestamp)
	}
	// Handle transaction fee fields based on type
	if tx.Type == 1 || (tx.MaxFee != nil && tx.MaxPriorityFee != nil) {
		pbTx.Type = 2 // EIP-1559
	} else {
		pbTx.Type = 0 // Legacy
		// For legacy transactions, use GasPrice as MaxFee if MaxFee is not set.
		if pbTx.MaxFee == "0" && tx.GasPrice != nil {
			pbTx.MaxFee = tx.GasPrice.String()
		}
	}

	return pbTx
}

// Helper function to convert config AccessList to []*pb.AccessTuple
func convertAccessListToPb(accessList config.AccessList) []*pb.AccessTuple {
	if len(accessList) == 0 {
		return nil
	}

	pbAccessList := make([]*pb.AccessTuple, len(accessList))
	for i, access := range accessList {
		storageKeys := make([]string, len(access.StorageKeys))
		for j, key := range access.StorageKeys {
			storageKeys[j] = key.Hex()
		}
		pbAccessList[i] = &pb.AccessTuple{
			Address:     access.Address.Hex(),
			StorageKeys: storageKeys,
		}
	}
	return pbAccessList
}

// Global mempool client instance
var globalMempoolClient *MempoolClient

// InitMempoolClient initializes the global mempool client
func InitMempoolClient(logger_ctx context.Context, address string) error {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("gRPCClient").Start(logger_ctx, "gRPCClient.InitMempoolClient")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("address", address))

	logger().NamedLogger.Info(spanCtx, "Initializing global mempool client",
		ion.String("address", address),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.InitMempoolClient"))

	client, err := NewMempoolClient(spanCtx, address)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "init_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return err
	}

	globalMempoolClient = client
	// Don't verify connection here since GetMempoolStats depends on routing client
	// which is initialized later in main.go

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Mempool client initialized successfully",
		ion.String("address", address),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "gRPCClient.InitMempoolClient"))

	return nil
}

// CloseMempoolClient closes the global mempool client
func CloseMempoolClient() {
	if globalMempoolClient != nil {
		globalMempoolClient.Close()
		globalMempoolClient = nil
	}
}

// SubmitToMempool submits a transaction to the mempool instead of propagating it directly
func SubmitToMempool(logger_ctx context.Context, tx *config.Transaction, txHash string) error {
	if globalMempoolClient == nil {
		return fmt.Errorf("mempool client not initialized")
	}

	return globalMempoolClient.SubmitTransaction(logger_ctx, tx, txHash)
}

func ReturnMempoolObject() (*MempoolClient, error) {
	if globalMempoolClient == nil {
		return nil, fmt.Errorf("mempool client not initialized. Call InitMempoolClient() first")
	}
	return globalMempoolClient, nil
}
