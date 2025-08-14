package gETH

import (
    "context"
    "fmt"
    "net"
    "os"
    "os/signal"
    "syscall"

    "github.com/rs/zerolog/log"
    "google.golang.org/grpc"
    "google.golang.org/grpc/reflection"
    "google.golang.org/grpc/health"
    "google.golang.org/grpc/health/grpc_health_v1"
	"gossipnode/gETH/proto"
)

// Server implements the gRPC Chain service
type Server struct {
    proto.UnimplementedChainServer
    proto.
}

// StartGRPC starts the gRPC server on the specified port
func StartGRPC(port int) {
    // Create a listener on the specified port
    lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
    if err != nil {
        log.Fatal().Err(err).Msg("Failed to create listener")
    }

    // Create a new gRPC server with default options
    grpcServer := grpc.NewServer(
        grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10MB max message size
    )

    // Register the service implementation
    server := &Server{}
    proto.RegisterChainServer(grpcServer, server)

    // Register health check service
    healthServer := health.NewServer()
    grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
    healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

    // Register reflection service for debugging
    reflection.Register(grpcServer)

    // Start the server in a goroutine
    go func() {
        log.Info().Int("port", port).Msg("gRPC server starting")
        if err := grpcServer.Serve(lis); err != nil {
            log.Fatal().Err(err).Msg("Failed to serve gRPC")
        }
    }()

    // Set up graceful shutdown
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
    
    // Block until we receive a shutdown signal
    <-stop
    log.Info().Msg("Shutting down gRPC server...")
    
    // Gracefully stop the server
    grpcServer.GracefulStop()
    healthServer.Shutdown()
    log.Info().Msg("gRPC server stopped")
}

// Implement the Chain service methods

func (s *Server) GetBlockByNumber(ctx context.Context, req *proto.GetBlockByNumberReq) (*proto.Block, error) {
	log.Info().Uint64("number", req.GetNumber()).Bool("fullTx", req.GetFullTx()).Msg("gRPC: GetBlockByNumber")
	block, err := _GetBlockByNumber(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetBlockByNumber failed")
		return nil, status.Errorf(codes.Internal, "failed to get block by number: %v", err)
	}
	return block, nil
}

func (s *Server) GetBlockByHash(ctx context.Context, req *proto.GetBlockByHashReq) (*proto.Block, error) {
	log.Info().Hex("hash", req.GetHash()).Bool("fullTx", req.GetFullTx()).Msg("gRPC: GetBlockByHash")
	block, err := _GetBlockByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetBlockByHash failed")
		return nil, status.Errorf(codes.Internal, "failed to get block by hash: %v", err)
	}
	return block, nil
}

func (s *Server) GetTransactionByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Transaction, error) {
	log.Info().Hex("hash", req.GetHash()).Msg("gRPC: GetTransactionByHash")
	tx, err := _GetTransactionByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetTransactionByHash failed")
		return nil, status.Errorf(codes.Internal, "failed to get transaction by hash: %v", err)
	}
	return tx, nil
}

func (s *Server) GetReceiptByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Receipt, error) {
	log.Info().Hex("hash", req.GetHash()).Msg("gRPC: GetReceiptByHash")
	receipt, err := _GetReceiptByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetReceiptByHash failed")
		return nil, status.Errorf(codes.Internal, "failed to get receipt by hash: %v", err)
	}
	return receipt, nil
}

func (s *Server) GetAccountState(ctx context.Context, req *proto.GetAccountStateReq) (*proto.AccountState, error) {
	log.Info().Hex("address", req.GetAddress()).Msg("gRPC: GetAccountState")
	accountState, err := _GetAccountState(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetAccountState failed")
		return nil, status.Errorf(codes.Internal, "failed to get account state: %v", err)
	}
	return accountState, nil
}

func (s *Server) SendRawTransaction(ctx context.Context, req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
	log.Info().Msg("gRPC: SendRawTransaction")
	resp, err := _SubmitRawTransaction(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: SendRawTransaction failed")
		return nil, status.Errorf(codes.Internal, "failed to submit raw transaction: %v", err)
	}
	return resp, nil
}

func (s *Server) GetLogs(ctx context.Context, req *proto.GetLogsReq) (*proto.GetLogsResp, error) {
	log.Warn().Msg("gRPC: GetLogs is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method GetLogs not implemented")
}

func (s *Server) Call(ctx context.Context, req *proto.CallReq) (*proto.CallResp, error) {
	log.Warn().Msg("gRPC: Call is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method Call not implemented")
}

func (s *Server) EstimateGas(ctx context.Context, req *proto.CallReq) (*proto.EstimateResp, error) {
	log.Warn().Msg("gRPC: EstimateGas is not implemented")
	return nil, status.Errorf(codes.Unimplemented, "method EstimateGas not implemented")
}

func (s *Server) StreamHeads(req *proto.Empty, stream proto.Chain_StreamHeadsServer) error {
	log.Warn().Msg("gRPC: StreamHeads is not implemented")
	return status.Errorf(codes.Unimplemented, "method StreamHeads not implemented")
}

func (s *Server) StreamLogs(req *proto.LogsSubReq, stream proto.Chain_StreamLogsServer) error {
	log.Warn().Msg("gRPC: StreamLogs is not implemented")
	return status.Errorf(codes.Unimplemented, "method StreamLogs not implemented")
}
