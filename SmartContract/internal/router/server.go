package router

import (
	"context"
	"fmt"
	"net"
	"strings"

	"gossipnode/SmartContract/proto"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// Server implements the SmartContract gRPC service
type Server struct {
	proto.UnimplementedSmartContractServiceServer
	router  *Router
	chainID int
}

// NewServer creates a new SmartContract gRPC server
func NewServer(router *Router) (*Server, error) {
	if router == nil {
		return nil, fmt.Errorf("router cannot be nil")
	}

	return &Server{
		router:  router,
		chainID: router.chainID,
	}, nil
}

// loopbackOnlyInterceptor rejects any request whose peer address is not a
// loopback address (127.x.x.x or ::1).  The SmartContract service is
// internal-only; exposing it to remote hosts would be a security risk.
func loopbackOnlyInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "no peer info")
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		// Fallback: treat the whole string as host
		host = p.Addr.String()
	}
	// Accept 127.x.x.x, ::1, and the abstract UDS path ("")
	if host != "" && !strings.HasPrefix(host, "127.") && host != "::1" && host != "localhost" {
		logger().Warn(ctx, "SmartContract: rejected non-loopback connection",
			ion.Err(fmt.Errorf("non-loopback peer: %s", p.Addr.String())),
			ion.String("peer", p.Addr.String()))
		return nil, status.Errorf(codes.PermissionDenied, "SmartContract service is only accessible from localhost")
	}
	return handler(ctx, req)
}

// StartGRPC starts the SmartContract gRPC server
func StartGRPC(ctx context.Context, port int, router *Router) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	logger().Info(ctx, "Starting SmartContract gRPC server",
		ion.Int("port", port))

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10*1024*1024), // 10MB max message size
		grpc.UnaryInterceptor(loopbackOnlyInterceptor),
	)

	server, err := NewServer(router)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	proto.RegisterSmartContractServiceServer(grpcServer, server)

	// Health check service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Enable reflection for debugging
	reflection.Register(grpcServer)

	// Handle shutdown when context is cancelled
	go func() {
		<-ctx.Done()
		logger().Info(context.Background(), "Shutting down SmartContract gRPC server...")
		grpcServer.GracefulStop()
		healthServer.Shutdown()
		logger().Info(context.Background(), "SmartContract gRPC server stopped")
	}()

	logger().Info(ctx, "SmartContract gRPC server started successfully")

	if err := grpcServer.Serve(lis); err != nil {
		// Ignore error if it's due to shutdown
		if ctx.Err() == nil {
			logger().Error(ctx, "gRPC server error", err)
			return err
		}
	}

	return nil
}

// ============================================================================
// gRPC Service Implementation
// ============================================================================

// CompileContract compiles Solidity source code
func (s *Server) CompileContract(ctx context.Context, req *proto.CompileRequest) (*proto.CompileResponse, error) {
	logger().Info(ctx, "Compiling contract",
		ion.String("compiler_version", req.CompilerVersion))

	contract, err := s.router.CompileContract(req.SourceCode)
	if err != nil {
		logger().Error(ctx, "Compilation failed", err)
		return &proto.CompileResponse{
			Error: err.Error(),
		}, nil
	}

	return &proto.CompileResponse{
		Contract: &proto.CompiledContract{
			Bytecode:         contract.Bytecode,
			Abi:              contract.ABI,
			DeployedBytecode: contract.DeployedBytecode,
			Name:             contract.Name,
		},
		CompilerVersion: "0.8.33",
	}, nil
}

// DeployContract deploys a compiled contract
func (s *Server) DeployContract(ctx context.Context, req *proto.DeployContractRequest) (*proto.DeployContractResponse, error) {
	result, err := s.router.DeployContract(ctx, req)
	if err != nil {
		logger().Error(ctx, "Deployment failed", err)
		return nil, err
	}

	return &proto.DeployContractResponse{
		Result: result,
	}, nil
}

// ExecuteContract executes a contract function
func (s *Server) ExecuteContract(ctx context.Context, req *proto.ExecuteContractRequest) (*proto.ExecuteContractResponse, error) {
	result, err := s.router.ExecuteContract(ctx, req)
	if err != nil {
		logger().Error(ctx, "Execution failed", err)
		return nil, err
	}

	return &proto.ExecuteContractResponse{
		Result: result,
	}, nil
}

// CallContract makes a read-only contract call
func (s *Server) CallContract(ctx context.Context, req *proto.CallContractRequest) (*proto.CallContractResponse, error) {
	returnData, err := s.router.CallContract(ctx, req)
	if err != nil {
		logger().Error(ctx, "Call failed", err)
		return &proto.CallContractResponse{
			Error: err.Error(),
		}, nil
	}

	return &proto.CallContractResponse{
		ReturnData: returnData,
	}, nil
}

// GetContractCode retrieves contract bytecode and metadata
func (s *Server) GetContractCode(ctx context.Context, req *proto.GetContractCodeRequest) (*proto.GetContractCodeResponse, error) {
	code, abi, metadata, err := s.router.GetContractCode(ctx, req.ContractAddress)
	if err != nil {
		logger().Error(ctx, "Failed to get contract code", err)
		return &proto.GetContractCodeResponse{}, err
	}

	return &proto.GetContractCodeResponse{
		Code:     code,
		Abi:      abi,
		Metadata: metadata,
	}, nil
}

// GetStorage retrieves a storage slot value
func (s *Server) GetStorage(ctx context.Context, req *proto.GetStorageRequest) (*proto.GetStorageResponse, error) {
	value, err := s.router.GetStorage(ctx, req.ContractAddress, req.StorageKey)
	if err != nil {
		logger().Error(ctx, "Failed to get storage", err)
		return &proto.GetStorageResponse{}, err
	}

	return &proto.GetStorageResponse{
		Value: value,
	}, nil
}

// EstimateGas estimates gas for a transaction
func (s *Server) EstimateGas(ctx context.Context, req *proto.EstimateGasRequest) (*proto.EstimateGasResponse, error) {
	gasEstimate, err := s.router.EstimateGas(ctx, req)
	if err != nil {
		logger().Error(ctx, "Gas estimation failed", err)
		return &proto.EstimateGasResponse{
			Error: err.Error(),
		}, nil
	}

	return &proto.EstimateGasResponse{
		GasEstimate: gasEstimate,
	}, nil
}

// EncodeFunctionCall encodes a function call to ABI bytes
func (s *Server) EncodeFunctionCall(ctx context.Context, req *proto.EncodeFunctionCallRequest) (*proto.EncodeFunctionCallResponse, error) {
	encoded, err := s.router.EncodeFunctionCall(req.AbiJson, req.FunctionName, req.Args)
	if err != nil {
		logger().Error(ctx, "Encoding failed", err)
		return &proto.EncodeFunctionCallResponse{
			Error: err.Error(),
		}, nil
	}

	return &proto.EncodeFunctionCallResponse{
		EncodedData: encoded,
	}, nil
}

// DecodeFunctionOutput decodes function output from ABI bytes
func (s *Server) DecodeFunctionOutput(ctx context.Context, req *proto.DecodeFunctionOutputRequest) (*proto.DecodeFunctionOutputResponse, error) {
	decoded, err := s.router.DecodeFunctionOutput(req.AbiJson, req.FunctionName, req.OutputData)
	if err != nil {
		logger().Error(ctx, "Decoding failed", err)
		return &proto.DecodeFunctionOutputResponse{
			Error: err.Error(),
		}, nil
	}

	return &proto.DecodeFunctionOutputResponse{
		DecodedValues: decoded,
	}, nil
}

// ListContracts lists deployed contracts
func (s *Server) ListContracts(ctx context.Context, req *proto.ListContractsRequest) (*proto.ListContractsResponse, error) {
	contracts, err := s.router.ListContracts(ctx, req.FromBlock, req.ToBlock, req.Limit)
	if err != nil {
		logger().Error(ctx, "Failed to list contracts", err)
		return &proto.ListContractsResponse{}, err
	}

	return &proto.ListContractsResponse{
		Contracts: contracts,
	}, nil
}

// Support StateDB passing for backward compatibility if needed, but NewServer now takes Router.
// We removed StateDB from NewServer signature to enforce Router pattern.
