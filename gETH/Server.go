package gETH

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"jmdn/config/GRO"
	"jmdn/config/settings"
	"jmdn/gETH/common"
	"jmdn/gETH/proto"
	"jmdn/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/rs/zerolog/log"
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
func StartGRPC(bindAddr string, port int, chainID int) error {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = common.InitializeGRO(GRO.GETHLocal)
		if err != nil {
			return fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	// Create a listener on the specified port
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Create secure gRPC server via gatekeeper helper
	secCfg := &settings.Get().Security
	grpcServer, serverTLS, err := gatekeeper.NewSecureGRPCServer(
		settings.ServiceEthGRPC, secCfg, nil,
		false,                             // no stream interceptor
		grpc.MaxRecvMsgSize(10*1024*1024), // 10MB max message size
	)
	if err != nil {
		return fmt.Errorf("failed to create secure gRPC server: %w", err)
	}
	if serverTLS != nil {
		log.Info().Msg("gETH gRPC server starting with TLS/mTLS enabled")
	} else {
		log.Warn().Msg("gETH gRPC server starting WITHOUT TLS (Insecure mode enabled in policy)")
	}

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
		log.Info().Int("port", port).Msg("gRPC server starting")
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatal().Err(err).Msg("Failed to serve gRPC")
			return fmt.Errorf("failed to serve gRPC: %w", err)
		}
		return nil
	})

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

	return nil
}

// Implement the Chain service methods

func (s *Server) GetBlockByNumber(ctx context.Context, req *proto.GetBlockByNumberReq) (*proto.Block, error) {
	log.Info().Uint64("number", req.GetNumber()).Bool("fullTx", req.GetFullTx()).Msg("gRPC: GetBlockByNumber")
	block, err := _GetBlockByNumber(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetBlockByNumber failed")
		return nil, fmt.Errorf("code: %d message: failed to get block by number: %v", codes.Internal, err)
	}
	return block, nil
}

func (s *Server) GetBlockByHash(ctx context.Context, req *proto.GetBlockByHashReq) (*proto.Block, error) {
	log.Info().Hex("hash", req.GetHash()).Bool("fullTx", req.GetFullTx()).Msg("gRPC: GetBlockByHash")
	block, err := _GetBlockByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetBlockByHash failed")
		return nil, fmt.Errorf("code: %d message: failed to get block by hash: %v", codes.Internal, err)
	}
	return block, nil
}

func (s *Server) GetTransactionByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Transaction, error) {
	log.Info().Hex("hash", req.GetHash()).Msg("gRPC: GetTransactionByHash")
	tx, err := _GetTransactionByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetTransactionByHash failed")
		return nil, fmt.Errorf("code: %d message: failed to get transaction by hash: %v", codes.Internal, err)
	}
	return tx, nil
}

func (s *Server) GetReceiptByHash(ctx context.Context, req *proto.GetByHashReq) (*proto.Receipt, error) {
	log.Info().Hex("hash", req.GetHash()).Msg("gRPC: GetReceiptByHash")
	receipt, err := _GetReceiptByHash(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetReceiptByHash failed")
		return nil, fmt.Errorf("code: %d message: failed to get receipt by hash: %v", codes.Internal, err)
	}
	return receipt, nil
}

func (s *Server) GetAccountState(ctx context.Context, req *proto.GetAccountStateReq) (*proto.AccountState, error) {
	log.Info().Hex("address", req.GetAddress()).Msg("gRPC: GetAccountState")
	accountState, err := _GetAccountState(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetAccountState failed")
		return nil, fmt.Errorf("code: %d message: failed to get account state: %v", codes.Internal, err)
	}
	return accountState, nil
}

func (s *Server) SendRawTransaction(ctx context.Context, req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
	log.Info().Msg("gRPC: SendRawTransaction")
	resp, err := _SubmitRawTransaction(req)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: SendRawTransaction failed")
		return nil, fmt.Errorf("code: %d message: failed to submit raw transaction: %v", codes.Internal, err)
	}
	return resp, nil
}

func (s *Server) GetLogs(ctx context.Context, req *proto.GetLogsReq) (*proto.GetLogsResp, error) {
	log.Warn().Msg("gRPC: GetLogs is not implemented")
	return nil, fmt.Errorf("code: %d message: method GetLogs not implemented", codes.Unimplemented)
}

func (s *Server) Call(ctx context.Context, req *proto.CallReq) (*proto.CallResp, error) {
	log.Warn().Msg("gRPC: Call is not implemented")
	return nil, fmt.Errorf("code: %d message: method Call not implemented", codes.Unimplemented)
}

func (s *Server) EstimateGas(ctx context.Context, req *proto.CallReq) (*proto.EstimateResp, error) {
	log.Warn().Msg("gRPC: EstimateGas is not implemented")
	return nil, fmt.Errorf("code: %d message: method EstimateGas not implemented", codes.Unimplemented)
}

func (s *Server) StreamHeads(req *proto.Empty, stream proto.Chain_StreamHeadsServer) error {
	log.Warn().Msg("gRPC: StreamHeads is not implemented")
	return fmt.Errorf("code: %d message: method StreamHeads not implemented", codes.Unimplemented)
}

func (s *Server) StreamLogs(req *proto.LogsSubReq, stream proto.Chain_StreamLogsServer) error {
	log.Warn().Msg("gRPC: StreamLogs is not implemented")
	return fmt.Errorf("code: %d message: method StreamLogs not implemented", codes.Unimplemented)
}

func (s *Server) GetChainID(ctx context.Context, req *proto.Empty) (*proto.Quantity, error) {
	log.Info().Msg("gRPC: GetChainID")
	quantity, err := _GetChainID(req, s.ChainID)
	if err != nil {
		log.Error().Err(err).Msg("gRPC: GetChainID failed")
		return nil, fmt.Errorf("code: %d message: failed to get chain ID: %v", codes.Internal, err)
	}
	return quantity, nil
}
