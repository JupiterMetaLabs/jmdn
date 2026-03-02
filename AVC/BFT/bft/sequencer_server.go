// =============================================================================
// FILE: pkg/bft/sequencer_server.go
// =============================================================================
package bft

import (
	"context"
	"fmt"
	"log"
	"net"

	pb "jmdn/AVC/BFT/proto"
	"jmdn/config/settings"
	"jmdn/pkg/gatekeeper"
)

// SequencerServer implements the gRPC server to receive results from buddies
type SequencerServer struct {
	pb.UnimplementedBFTServiceServer

	client *SequencerBFTClient
}

// NewSequencerServer creates a new Sequencer gRPC server
func NewSequencerServer(client *SequencerBFTClient) *SequencerServer {
	return &SequencerServer{
		client: client,
	}
}

// InitiateBFT - Not used on sequencer side, buddies call this
func (s *SequencerServer) InitiateBFT(
	ctx context.Context,
	req *pb.BFTRequest,
) (*pb.BFTResponse, error) {
	return &pb.BFTResponse{
		Accepted: false,
		Message:  "Sequencer does not accept BFT requests",
	}, nil
}

// ReportResult receives results from buddy nodes
func (s *SequencerServer) ReportResult(
	ctx context.Context,
	result *pb.BFTResult,
) (*pb.ResultAck, error) {

	log.Printf("📥 [Sequencer] Received result from %s", result.BuddyId)
	log.Printf("   Success: %v", result.Success)
	log.Printf("   Decision: %s", result.Decision)
	log.Printf("   Block Accepted: %v", result.BlockAccepted)

	// Record the result in the client
	s.client.RecordResult(result)

	return &pb.ResultAck{
		Received: true,
		Message:  "Result received successfully",
	}, nil
}

// StartServer starts the Sequencer gRPC server
func (s *SequencerServer) StartServer(port int) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	ionLogger := logger()

	// Create secure gRPC server via gatekeeper helper
	secCfg := &settings.Get().Security
	grpcServer, serverTLS, err := gatekeeper.NewSecureGRPCServer(
		settings.ServiceBFTSequencer, secCfg, ionLogger, false,
	)
	if err != nil {
		return fmt.Errorf("failed to create secure gRPC server: %w", err)
	}
	if serverTLS != nil && ionLogger != nil {
		ionLogger.Info(context.Background(), "TLS Enabled for BFT Sequencer Service")
	}
	pb.RegisterBFTServiceServer(grpcServer, s)

	log.Printf("🎧 [Sequencer] gRPC server listening on port %d (Secured)", port)

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve: %w", err)
	}

	return nil
}
