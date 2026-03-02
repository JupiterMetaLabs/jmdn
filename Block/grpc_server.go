package Block

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	BlockCommon "jmdn/Block/common"
	pb "jmdn/Block/proto"
	"jmdn/Sequencer"
	"jmdn/config"
	GRO "jmdn/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"jmdn/config/settings"
	"jmdn/pkg/gatekeeper"
)

var LocalGRO interfaces.LocalGoroutineManagerInterface

// BlockServer implements the gRPC BlockService
type BlockServer struct {
	pb.UnimplementedBlockServiceServer
	host    host.Host
	chainID int
	logger  zerolog.Logger
}

// NewBlockServer creates a new BlockServer instance
func NewBlockServer(h host.Host, chainID int) *BlockServer {
	// Configure zerolog for gRPC server to log to stdout
	grpcLogger := zerolog.New(os.Stdout).With().
		Timestamp().
		Str("component", "block-grpc").
		Logger()

	return &BlockServer{
		host:    h,
		chainID: chainID,
		logger:  grpcLogger,
	}
}

// ProcessBlock handles the gRPC ProcessBlock request
func (s *BlockServer) ProcessBlock(ctx context.Context, req *pb.ProcessBlockRequest) (*pb.ProcessBlockResponse, error) {
	s.logger.Info().
		Uint64("block_number", req.Block.BlockNumber).
		Str("block_hash", common.Bytes2Hex(req.Block.BlockHash)).
		Int("tx_count", len(req.Block.Transactions)).
		Msg("gRPC: ProcessBlock request received")

	// Convert proto block to config.ZKBlock
	block, err := s.convertProtoToZKBlock(req.Block)
	if err != nil {
		s.logger.Error().Err(err).Msg("gRPC: Failed to convert proto block to ZKBlock")
		return nil, status.Errorf(codes.InvalidArgument, "invalid block data: %v", err)
	}

	// Validate block data
	if len(block.Transactions) == 0 {
		s.logger.Error().Msg("gRPC: Block contains no transactions")
		return nil, status.Errorf(codes.InvalidArgument, "block contains no transactions")
	}

	if block.Status != "verified" {
		s.logger.Error().Str("status", block.Status).Msg("gRPC: Block not verified")
		return nil, status.Errorf(codes.InvalidArgument, "block has not been verified by ZKVM")
	}

	// Create consensus instance and start consensus process
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, s.host)
	// Debugging
	fmt.Printf("Consensus: %+v\n", consensus)
	if err := consensus.Start(block); err != nil {
		fmt.Printf("Error starting consensus process: %+v\n", err)
		s.logger.Error().
			Err(err).
			Str("block_hash", block.BlockHash.Hex()).
			Msg("gRPC: Failed to start consensus process")
		return nil, status.Errorf(codes.Internal, "failed to start consensus process: %v", err)
	}

	// Log transactions
	for _, tx := range block.Transactions {
		LogTransaction(
			tx.Hash.Hex(),
			tx.From.Hex(),
			tx.To.Hex(),
			tx.Value.String(),
			fmt.Sprintf("%d", tx.Type),
		)
	}

	s.logger.Info().
		Uint64("block_number", block.BlockNumber).
		Str("block_hash", block.BlockHash.Hex()).
		Int("tx_count", len(block.Transactions)).
		Msg("gRPC: Block processed successfully")

	// Return success response
	return &pb.ProcessBlockResponse{
		Success:     true,
		Message:     fmt.Sprintf("Block %d with %d transactions processed and propagated successfully", block.BlockNumber, len(block.Transactions)),
		BlockHash:   block.BlockHash.Hex(),
		BlockNumber: block.BlockNumber,
	}, nil
}

// StartGRPCServer starts the gRPC server on the specified port
func StartGRPCServer(bindAddr string, port int, h host.Host, chainID int) error {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = BlockCommon.InitializeGRO(GRO.BlockGRPCServerLocal)
		if err != nil {
			return fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	// Load Security Configuration
	secCfg := &settings.Get().Security

	// Create secure gRPC server via gatekeeper helper
	// Block server needs stream interceptor + large message sizes for block data
	grpcServer, serverTLS, err := gatekeeper.NewSecureGRPCServer(
		settings.ServiceBlockIngestGRPC, secCfg, nil,
		true,                              // includeStreamInterceptor — Block uses streaming RPCs
		grpc.MaxRecvMsgSize(50*1024*1024), // 50MB max message size for blocks
		grpc.MaxSendMsgSize(50*1024*1024), // 50MB max send size
	)
	if err != nil {
		return fmt.Errorf("failed to create secure gRPC server: %w", err)
	}
	if serverTLS != nil {
		log.Info().Msg("BlockGRPC server using mTLS/TLS")
	} else {
		log.Warn().Msg("BlockGRPC server starting INSECUREly (TLS disabled in policy)")
	}

	// Create and register the BlockServer
	server := NewBlockServer(h, chainID)
	pb.RegisterBlockServiceServer(grpcServer, server)

	// Register health check service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register reflection service for debugging
	reflection.Register(grpcServer)

	// Start the server in a goroutine
	LocalGRO.Go(GRO.BlockGRPCServerThread, func(ctx context.Context) error {
		log.Info().Int("port", port).Msg("Block gRPC server starting")
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatal().Err(err).Msg("Failed to serve Block gRPC")
		}
		return nil
	})

	// Set up graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Block until we receive a shutdown signal
	<-stop
	log.Info().Msg("Shutting down Block gRPC server...")

	// Gracefully stop the server
	grpcServer.GracefulStop()
	healthServer.Shutdown()
	log.Info().Msg("Block gRPC server stopped")

	return nil
}

// convertProtoToZKBlock converts a proto ZKBlock to config.ZKBlock
func (s *BlockServer) convertProtoToZKBlock(pbBlock *pb.ZKBlock) (*config.ZKBlock, error) {
	// Convert transactions
	txs := make([]config.Transaction, len(pbBlock.Transactions))
	for i, pbTx := range pbBlock.Transactions {
		tx, err := convertProtoToTransaction(pbTx)
		if err != nil {
			return nil, fmt.Errorf("failed to convert transaction %d: %w", i, err)
		}
		txs[i] = *tx
	}

	block := &config.ZKBlock{
		StarkProof:   pbBlock.StarkProof,
		Commitment:   pbBlock.Commitment,
		ProofHash:    pbBlock.ProofHash,
		Status:       pbBlock.Status,
		TxnsRoot:     pbBlock.TxnsRoot,
		Transactions: txs,
		Timestamp:    pbBlock.Timestamp,
		ExtraData:    pbBlock.ExtraData,
		StateRoot:    common.BytesToHash(pbBlock.StateRoot),
		LogsBloom:    pbBlock.LogsBloom,
		PrevHash:     common.BytesToHash(pbBlock.PrevHash),
		BlockHash:    common.BytesToHash(pbBlock.BlockHash),
		GasLimit:     pbBlock.GasLimit,
		GasUsed:      pbBlock.GasUsed,
		BlockNumber:  pbBlock.BlockNumber,
	}

	// Convert addresses if they're not empty
	if len(pbBlock.CoinbaseAddr) > 0 {
		addr := common.BytesToAddress(pbBlock.CoinbaseAddr)
		block.CoinbaseAddr = &addr
	}
	if len(pbBlock.ZkvmAddr) > 0 {
		addr := common.BytesToAddress(pbBlock.ZkvmAddr)
		block.ZKVMAddr = &addr
	}

	return block, nil
}

// convertProtoToTransaction converts a proto Transaction to config.Transaction
func convertProtoToTransaction(pbTx *pb.Transaction) (*config.Transaction, error) {
	tx := &config.Transaction{
		Hash:      common.BytesToHash(pbTx.Hash),
		Type:      uint8(pbTx.Type),
		Timestamp: pbTx.Timestamp,
		Nonce:     pbTx.Nonce,
		GasLimit:  pbTx.GasLimit,
		Data:      pbTx.Data,
	}

	// Convert addresses
	if len(pbTx.From) > 0 {
		addr := common.BytesToAddress(pbTx.From)
		tx.From = &addr
	}
	if len(pbTx.To) > 0 {
		addr := common.BytesToAddress(pbTx.To)
		tx.To = &addr
	}

	// Convert big.Int fields
	if len(pbTx.Value) > 0 {
		tx.Value = newIntFromBytes(pbTx.Value)
	}
	if len(pbTx.ChainId) > 0 {
		tx.ChainID = newIntFromBytes(pbTx.ChainId)
	}
	if len(pbTx.GasPrice) > 0 {
		tx.GasPrice = newIntFromBytes(pbTx.GasPrice)
	}
	if len(pbTx.MaxFee) > 0 {
		tx.MaxFee = newIntFromBytes(pbTx.MaxFee)
	}
	if len(pbTx.MaxPriorityFee) > 0 {
		tx.MaxPriorityFee = newIntFromBytes(pbTx.MaxPriorityFee)
	}

	// Convert signature fields
	if len(pbTx.V) > 0 {
		tx.V = newIntFromBytes(pbTx.V)
	}
	if len(pbTx.R) > 0 {
		tx.R = newIntFromBytes(pbTx.R)
	}
	if len(pbTx.S) > 0 {
		tx.S = newIntFromBytes(pbTx.S)
	}

	// Convert access list
	if pbTx.AccessList != nil {
		tx.AccessList = convertProtoToAccessList(pbTx.AccessList)
	}

	return tx, nil
}

// convertProtoToAccessList converts a proto AccessList to config.AccessList
func convertProtoToAccessList(pbAL *pb.AccessList) config.AccessList {
	if pbAL == nil {
		return config.AccessList{}
	}

	accessList := make(config.AccessList, len(pbAL.AccessTuples))
	for i, pbTuple := range pbAL.AccessTuples {
		storageKeys := make([]common.Hash, len(pbTuple.StorageKeys))
		for j, key := range pbTuple.StorageKeys {
			storageKeys[j] = common.BytesToHash(key)
		}

		accessList[i] = config.AccessTuple{
			Address:     common.BytesToAddress(pbTuple.Address),
			StorageKeys: storageKeys,
		}
	}

	return accessList
}

// Helper function to create *big.Int from bytes
func newIntFromBytes(b []byte) *big.Int {
	if len(b) == 0 {
		return newIntFromString("0")
	}

	// First, check if the bytes represent an ASCII numeric string
	// This handles cases where big.Int was serialized as a string in JSON/protobuf
	if isASCIIString(b) {
		chainIDStr := strings.TrimSpace(string(b))
		// Debug: print the bytes and string representation
		fmt.Printf("DEBUG newIntFromBytes: bytes (hex): %x, bytes (ASCII): %s\n", b, chainIDStr)
		// Try parsing as decimal string first
		result := new(big.Int)
		if _, ok := result.SetString(chainIDStr, 10); ok {
			fmt.Printf("DEBUG newIntFromBytes: parsed as decimal string: %s -> %s\n", chainIDStr, result.String())
			return result
		}
		// If decimal fails, try hex (with or without 0x prefix)
		chainIDStr = strings.TrimPrefix(chainIDStr, "0x")
		if _, ok := result.SetString(chainIDStr, 16); ok {
			fmt.Printf("DEBUG newIntFromBytes: parsed as hex string: %s -> %s\n", chainIDStr, result.String())
			return result
		}
		fmt.Printf("DEBUG newIntFromBytes: failed to parse as string, falling back to byte interpretation\n")
		// If both fail, fall through to byte interpretation
	}

	// Default: interpret as big-endian integer bytes
	result := new(big.Int)
	result.SetBytes(b)
	fmt.Printf("DEBUG newIntFromBytes: interpreted as big-endian bytes: %x -> %s\n", b, result.String())
	return result
}

// isASCIIString checks if bytes represent an ASCII string (all printable ASCII)
func isASCIIString(b []byte) bool {
	for _, byteVal := range b {
		// Check if byte is printable ASCII (space ' ' to tilde '~')
		if byteVal < 32 || byteVal > 126 {
			// If it contains non-printable characters, likely not an ASCII string
			// But allow common whitespace
			if byteVal != '\n' && byteVal != '\r' && byteVal != '\t' {
				return false
			}
		}
	}
	return true
}

// Helper function to safely convert string to big.Int
func newIntFromString(s string) *big.Int {
	result := new(big.Int)
	result.SetString(s, 10)
	return result
}
