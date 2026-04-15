package gETH

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"gossipnode/gETH/common"
	"gossipnode/config/GRO"
	"gossipnode/gETH/proto"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"github.com/cockroachdb/errors/grpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)
var LocalGRO interfaces.LocalGoroutineManagerInterface
// Server implements the gRPC Chain service
type Server struct {
	proto.UnimplementedChainServer
	ChainID int
}

// StartGRPC starts the gRPC server on the specified port
func StartGRPC(port int, chainID int) error {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = common.InitializeGRO(GRO.GETHLocal)
		if err != nil {
			return fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	// Create a listener on the specified port
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Create a new gRPC server with default options
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10MB max message size
	)

	// Register the service implementation
	server := &Server{
		ChainID: chainID,
	}
	proto.RegisterChainServer(grpcServer, server)

	// Register health check service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register reflection service for debugging
	reflection.Register(grpcServer)

	// Start the server in a goroutine
	LocalGRO.Go(GRO.GETHgRPCThread, func(ctx context.Context) error {
		logger().Info(ctx, "gRPC server starting",
			ion.Int("port", port))
		if err := grpcServer.Serve(lis); err != nil {
			logger().Error(ctx, "Failed to serve gRPC", err)
			return fmt.Errorf("failed to serve gRPC: %w", err)
		}
		return nil
	})

	// Set up graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Block until we receive a shutdown signal
	<-stop
	logger().Info(context.Background(), "Shutting down gRPC server...")

	// Gracefully stop the server
	grpcServer.GracefulStop()
	healthServer.Shutdown()
	logger().Info(context.Background(), "gRPC server stopped")

	return nil
}

// Implement the Chain service methods

func (s *Server) GetBlockByNumber(ctx context.Context, req *proto.GetBlockByNumberReq) (*proto.Block, error) {
	logger().Info(ctx, "gRPC: GetBlockByNumber",
		ion.Uint64("number", req.GetNumber()),
		ion.Bool("fullTx", req.GetFullTx()))
	block, err := _GetBlockByNumber(req)
	if err != nil {
		logger().Error(ctx, "gRPC: GetBlockByNumber failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get block by number: %v", err)
	}
	return block, nil
}

func (s *Server) GetBlockByHash(ctx context.Context, req *proto.GetBlockByHashReq) (*proto.Block, error) {
	logger().Info(ctx, "gRPC: GetBlockByHash",
		ion.String("hash", fmt.Sprintf("%x", req.GetHash())),
		ion.Bool("fullTx", req.GetFullTx()))
	block, err := _GetBlockByHash(req)
	if err != nil {
		logger().Error(ctx, "gRPC: GetBlockByHash failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get block by hash: %v", err)
	}
	return block, nil
}

func (s *Server) GetTransactionByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Transaction, error) {
	logger().Info(ctx, "gRPC: GetTransactionByHash",
		ion.String("hash", fmt.Sprintf("%x", req.GetHash())))
	tx, err := _GetTransactionByHash(req)
	if err != nil {
		logger().Error(ctx, "gRPC: GetTransactionByHash failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get transaction by hash: %v", err)
	}
	return tx, nil
}

func (s *Server) GetReceiptByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Receipt, error) {
	logger().Info(ctx, "gRPC: GetReceiptByHash",
		ion.String("hash", fmt.Sprintf("%x", req.GetHash())))
	receipt, err := _GetReceiptByHash(req)
	if err != nil {
		logger().Error(ctx, "gRPC: GetReceiptByHash failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get receipt by hash: %v", err)
	}
	return receipt, nil
}

func (s *Server) GetAccountState(ctx context.Context, req *proto.GetAccountStateReq) (*proto.AccountState, error) {
	logger().Info(ctx, "gRPC: GetAccountState",
		ion.String("address", fmt.Sprintf("%x", req.GetAddress())))
	accountState, err := _GetAccountState(req)
	if err != nil {
		logger().Error(ctx, "gRPC: GetAccountState failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get account state: %v", err)
	}
	return accountState, nil
}

func (s *Server) SendRawTransaction(ctx context.Context, req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
	logger().Info(ctx, "gRPC: SendRawTransaction")
	resp, err := _SubmitRawTransaction(req)
	if err != nil {
		logger().Error(ctx, "gRPC: SendRawTransaction failed", err)
		return nil, status.Errorf(codes.Internal, "failed to submit raw transaction: %v", err)
	}
	return resp, nil
}

func (s *Server) GetLogs(ctx context.Context, req *proto.GetLogsReq) (*proto.GetLogsResp, error) {
	logger().Warn(ctx, "gRPC: GetLogs is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method GetLogs not implemented")
}

func (s *Server) Call(ctx context.Context, req *proto.CallReq) (*proto.CallResp, error) {
	logger().Warn(ctx, "gRPC: Call is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method Call not implemented")
}

func (s *Server) EstimateGas(ctx context.Context, req *proto.CallReq) (*proto.EstimateResp, error) {
	logger().Warn(ctx, "gRPC: EstimateGas is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method EstimateGas not implemented")
}

func (s *Server) StreamHeads(req *proto.Empty, stream proto.Chain_StreamHeadsServer) error {
	logger().Warn(context.Background(), "gRPC: StreamHeads is not implemented")
	return status.Errorf(codes.Unimplemented, "method StreamHeads not implemented")
}

func (s *Server) StreamLogs(req *proto.LogsSubReq, stream proto.Chain_StreamLogsServer) error {
	logger().Warn(context.Background(), "gRPC: StreamLogs is not implemented")
	return status.Errorf(codes.Unimplemented, "method StreamLogs not implemented")
}

func (s *Server) GetChainID(ctx context.Context, req *proto.Empty) (*proto.Quantity, error) {
	logger().Info(ctx, "gRPC: GetChainID")
	quantity, err := _GetChainID(req, s.ChainID)
	if err != nil {
		logger().Error(ctx, "gRPC: GetChainID failed", err)
		return nil, status.Errorf(codes.Internal, "failed to get chain ID: %v", err)
	}
	return quantity, nil
}
