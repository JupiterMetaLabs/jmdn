package Block

import (
	"context"
	"fmt"
	"time"

	pb "gossipnode/Mempool/proto"
	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/ion"
	"github.com/golang/protobuf/ptypes/empty"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
)

// Define singleton function to init the routing client
var routingclient *RoutingClient

// RoutingClient
type RoutingClient struct {
	client pb.RoutingServiceClient
	conn   *grpc.ClientConn
}

// Builder function to set the routing client and get the data from the routing client
func NewRoutingServiceClient(logger_ctx context.Context, address string) (*RoutingClient, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(logger_ctx, "RoutingClient.NewRoutingServiceClient")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("address", address))

	logger().NamedLogger.Debug(spanCtx, "Creating new routing service client",
		ion.String("address", address),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.NewRoutingServiceClient"))

	// If routingclient is not nil, return the existing routing client (singleton pattern)
	if routingclient != nil {
		span.SetAttributes(attribute.String("status", "reused_singleton"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Debug(spanCtx, "Reusing existing routing client (singleton)",
			ion.String("address", address),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.NewRoutingServiceClient"))
		return routingclient, nil
	}

	// 1. Setup TLS Loader
	secCfg := &settings.Get().Security
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger().NamedLogger)

	// 2. Load Client Credentials (Standardized Helper)
	// We identify as "mempool_client" connecting to ServiceMempool (Routing shares Mempool endpoint logic)
	creds, err := tlsLoader.LoadClientCredentials(settings.ServiceMempool, "mempool_client")
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "tls_config_failed"))
		logger().NamedLogger.Error(spanCtx, "Failed to load TLS credentials for Routing Client", err,
			ion.String("address", address))
		return nil, fmt.Errorf("failed to load TLS credentials for Routing Client: %w", err)
	}

	logger().NamedLogger.Info(spanCtx, "Routing Client Credentials Loaded", ion.String("address", address))

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to connect to Routing Service",
			err,
			ion.String("address", address),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.NewRoutingServiceClient"))
		return nil, fmt.Errorf("failed to connect to Routing Service: %v", err)
	}

	client := pb.NewRoutingServiceClient(conn)

	// Create new routing client and assign to singleton
	routingclient = &RoutingClient{
		client: client,
		conn:   conn,
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Debug(spanCtx, "Successfully created routing service client",
		ion.String("address", address),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.NewRoutingServiceClient"))

	return routingclient, nil
}

// GetRoutingClient returns the singleton routing client instance
func GetRoutingClient(logger_ctx context.Context) (*RoutingClient, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(logger_ctx, "RoutingClient.GetRoutingClient")
	defer span.End()

	startTime := time.Now().UTC()

	logger().NamedLogger.Debug(spanCtx, "Getting routing client",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetRoutingClient"))

	if routingclient == nil {
		span.RecordError(fmt.Errorf("routing client is nil"))
		span.SetAttributes(attribute.String("status", "nil_client"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Routing client is nil",
			fmt.Errorf("routing client is nil"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "RoutingClient.GetRoutingClient"))
		return nil, fmt.Errorf("routing client is nil")
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Debug(spanCtx, "Successfully retrieved routing client",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetRoutingClient"))

	return routingclient, nil
}

// SetRoutingClient sets the singleton routing client (mainly for testing or manual override)
func SetRoutingClient(client *RoutingClient) {
	routingclient = client
}

// GetFeeStatistics gets fee statistics from the routing service
func (r *RoutingClient) GetFeeStatistics(logger_ctx context.Context) (*pb.FeeStatistics, error) {
	// Record trace span and close it
	ctx, cancel := context.WithTimeout(logger_ctx, 5*time.Second)
	defer cancel()

	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(ctx, "RoutingClient.GetFeeStatistics")
	defer span.End()

	startTime := time.Now().UTC()

	logger().NamedLogger.Debug(spanCtx, "Getting fee statistics from routing service",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetFeeStatistics"))

	stats, err := r.client.GetFeeStatistics(ctx, &empty.Empty{})
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
			ion.String("function", "RoutingClient.GetFeeStatistics"))
		return nil, err
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
	logger().NamedLogger.Debug(spanCtx, "Successfully retrieved fee statistics",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetFeeStatistics"))

	return stats, nil
}

func (r *RoutingClient) GetMempoolStats(logger_ctx context.Context) (*pb.MREStats, error) {
	// Record trace span and close it
	ctx, cancel := context.WithTimeout(logger_ctx, 5*time.Second)
	defer cancel()

	spanCtx, span := logger().NamedLogger.Tracer("RoutingClient").Start(ctx, "RoutingClient.GetMempoolStats")
	defer span.End()

	startTime := time.Now().UTC()

	logger().NamedLogger.Debug(spanCtx, "Getting mempool stats from routing service",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetMempoolStats"))

	stats, err := r.client.GetMempoolStats(ctx, &empty.Empty{})
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
			ion.String("function", "RoutingClient.GetMempoolStats"))
		return nil, err
	}

	span.SetAttributes(attribute.String("stats_retrieved", "true"))

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Debug(spanCtx, "Successfully retrieved mempool stats",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "RoutingClient.GetMempoolStats"))

	return stats, nil
}
