package router

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// Server implements the SmartContract gRPC service
type Server struct {
	proto.UnimplementedSmartContractServiceServer
	router  *SmartContractRouter
	chainID int
}

// NewServer creates a new SmartContract gRPC server
func NewServer(chainID int, stateDB vm.StateDB) (*Server, error) {
	router, err := NewSmartContractRouter(chainID, stateDB)
	if err != nil {
		return nil, fmt.Errorf("failed to create router: %w", err)
	}

	return &Server{
		router:  router,
		chainID: chainID,
	}, nil
}

// StartGRPC starts the SmartContract gRPC server
func StartGRPC(port int, chainID int, stateDB vm.StateDB) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	log.Info().Int("port", port).Msg("Starting SmartContract gRPC server")

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10MB max message size
	)

	server, err := NewServer(chainID, stateDB)
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

	// Start server in goroutine
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Error().Err(err).Msg("gRPC server error")
		}
	}()

	log.Info().Msg("SmartContract gRPC server started successfully")

	// Wait for shutdown signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop
	log.Info().Msg("Shutting down SmartContract gRPC server...")

	grpcServer.GracefulStop()
	healthServer.Shutdown()
	log.Info().Msg("SmartContract gRPC server stopped")

	return nil
}

// ============================================================================
// gRPC Service Implementation
// ============================================================================

// CompileContract compiles Solidity source code
func (s *Server) CompileContract(ctx context.Context, req *proto.CompileRequest) (*proto.CompileResponse, error) {
	log.Info().Str("compiler_version", req.CompilerVersion).Msg("Compiling contract")

	contract, err := s.router.CompileContract(req.SourceCode)
	if err != nil {
		log.Error().Err(err).Msg("Compilation failed")
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
	log.Info().
		Hex("caller", req.Caller).
		Uint64("gas_limit", req.GasLimit).
		Msg("Deploying contract")

	// Convert request to router parameters
	result, err := s.router.DeployContract(req)
	if err != nil {
		log.Error().Err(err).Msg("Deployment failed")
		return &proto.DeployContractResponse{
			Result: &proto.ExecutionResult{
				Error:   err.Error(),
				Success: false,
			},
		}, nil
	}

	return &proto.DeployContractResponse{
		Result: result,
	}, nil
}

// ExecuteContract executes a contract function
func (s *Server) ExecuteContract(ctx context.Context, req *proto.ExecuteContractRequest) (*proto.ExecuteContractResponse, error) {
	log.Info().
		Hex("caller", req.Caller).
		Hex("contract", req.ContractAddress).
		Uint64("gas_limit", req.GasLimit).
		Msg("Executing contract")

	result, err := s.router.ExecuteContract(req)
	if err != nil {
		log.Error().Err(err).Msg("Execution failed")
		return &proto.ExecuteContractResponse{
			Result: &proto.ExecutionResult{
				Error:   err.Error(),
				Success: false,
			},
		}, nil
	}

	return &proto.ExecuteContractResponse{
		Result: result,
	}, nil
}

// CallContract makes a read-only contract call
func (s *Server) CallContract(ctx context.Context, req *proto.CallContractRequest) (*proto.CallContractResponse, error) {
	log.Debug().
		Hex("caller", req.Caller).
		Hex("contract", req.ContractAddress).
		Msg("Calling contract (read-only)")

	returnData, err := s.router.CallContract(req)
	if err != nil {
		log.Error().Err(err).Msg("Call failed")
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
	log.Debug().Hex("contract", req.ContractAddress).Msg("Getting contract code")

	code, abi, metadata, err := s.router.GetContractCode(req.ContractAddress)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get contract code")
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
	log.Debug().
		Hex("contract", req.ContractAddress).
		Hex("key", req.StorageKey).
		Msg("Getting storage")

	value, err := s.router.GetStorage(req.ContractAddress, req.StorageKey)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get storage")
		return &proto.GetStorageResponse{}, err
	}

	return &proto.GetStorageResponse{
		Value: value,
	}, nil
}

// EstimateGas estimates gas for a transaction
func (s *Server) EstimateGas(ctx context.Context, req *proto.EstimateGasRequest) (*proto.EstimateGasResponse, error) {
	log.Debug().Hex("caller", req.Caller).Msg("Estimating gas")

	gasEstimate, err := s.router.EstimateGas(req)
	if err != nil {
		log.Error().Err(err).Msg("Gas estimation failed")
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
	log.Debug().Str("function", req.FunctionName).Msg("Encoding function call")

	encoded, err := s.router.EncodeFunctionCall(req.AbiJson, req.FunctionName, req.Args)
	if err != nil {
		log.Error().Err(err).Msg("Encoding failed")
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
	log.Debug().Str("function", req.FunctionName).Msg("Decoding function output")

	decoded, err := s.router.DecodeFunctionOutput(req.AbiJson, req.FunctionName, req.OutputData)
	if err != nil {
		log.Error().Err(err).Msg("Decoding failed")
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
	log.Debug().Uint64("from", req.FromBlock).Uint64("to", req.ToBlock).Msg("Listing contracts")

	contracts, err := s.router.ListContracts(req.FromBlock, req.ToBlock, req.Limit)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list contracts")
		return &proto.ListContractsResponse{}, err
	}

	return &proto.ListContractsResponse{
		Contracts: contracts,
	}, nil
}
