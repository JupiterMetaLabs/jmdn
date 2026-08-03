package Block

import (
	"context"
	"fmt"
	pb "gossipnode/Mempool/proto"
	"gossipnode/logging"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Define singleton function to init the routing client
var routingclient *RoutingClient

// RoutingClient
type RoutingClient struct {
	client pb.RoutingServiceClient
	conn   *grpc.ClientConn
	logger *logging.AsyncLogger
}

// Builder function to set the routing client and get the data from the routing client
func NewRoutingServiceClient(address string) (*RoutingClient, error) {
	// If routingclient is not nil, return the existing routing client (singleton pattern)
	if routingclient != nil {
		return routingclient, nil
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))

	if err != nil {
		return nil, fmt.Errorf("failed to connect to Routing Service: %v", err)
	}
	client := pb.NewRoutingServiceClient(conn)

	Logger := logging.NewAsyncLogger()

	// Create new routing client and assign to singleton
	routingclient = &RoutingClient{
		client: client,
		conn:   conn,
		logger: Logger,
	}

	return routingclient, nil
}

// GetRoutingClient returns the singleton routing client instance
func GetRoutingClient() (*RoutingClient, error) {
	if routingclient == nil {
		return nil, fmt.Errorf("routing client is nil")
	}
	return routingclient, nil
}

// SetRoutingClient sets the singleton routing client (mainly for testing or manual override)
func SetRoutingClient(client *RoutingClient) {
	routingclient = client
}

// GetFeeStatistics gets fee statistics from the routing service
func (r *RoutingClient) GetFeeStatistics(ctx context.Context) (*pb.FeeStatistics, error) {
	return r.client.GetFeeStatistics(ctx, &empty.Empty{})
}

func (r *RoutingClient) GetMempoolStats(ctx context.Context) (*pb.MREStats, error) {
	return r.client.GetMempoolStats(ctx, &empty.Empty{})
}

// PeekPendingTransactions calls the MRE v1 service (non-destructive read).
// It uses the /jmdt.proto.mre.v1.MREService/PeekPendingTransactions path which
// is registered alongside the legacy RoutingService on the same gRPC server.
// The response is decoded into TransactionBatch — wire-compatible with MRE v1's
// GetPendingResponse because both carry repeated Transaction at field 1.
func (r *RoutingClient) PeekPendingTransactions(logger_ctx context.Context, limit int32) (*pb.TransactionBatch, error) {
	ctx, cancel := context.WithTimeout(logger_ctx, 5*time.Second)
	defer cancel()

	req := &pb.GetPendingRequest{Limit: limit}
	out := &pb.TransactionBatch{}
	err := r.conn.Invoke(ctx, "/jmdt.proto.mre.v1.MREService/PeekPendingTransactions", req, out, grpc.StaticMethod())
	if err != nil {
		return nil, fmt.Errorf("PeekPendingTransactions: %w", err)
	}
	return out, nil
}
