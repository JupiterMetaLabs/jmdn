// =============================================================================
// FILE: pkg/bft/buddy_service.go
// =============================================================================
package bft

import (
	"context"
	"crypto/tls"
	"fmt"
	"gossipnode/AVC/BFT/common"
	pb "gossipnode/AVC/BFT/proto"
	"log"
	"net"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// =============================================================================
// BuddyService implements the BFT gRPC service for buddy nodes
// =============================================================================
type BuddyService struct {
	pb.UnimplementedBFTServiceServer

	buddyID      string
	privateKey   []byte
	libp2pHost   host.Host
	gps          *PubSubMessages.GossipPubSub // Your existing GossipPubSub instance
	sequencerURL string

	bftConfig Config
	signer    Signer
	tlsCfg    *tls.Config
}

// =============================================================================
// Constructor
// =============================================================================

// NewBuddyService creates a new buddy node BFT service.
func NewBuddyService(
	buddyID string,
	privateKey []byte,
	libp2pHost host.Host,
	gps *PubSubMessages.GossipPubSub, // Pass your existing GossipPubSub
	sequencerURL string,
	tlsCfg *tls.Config,
) *BuddyService {

	config := DefaultConfig()

	return &BuddyService{
		buddyID:      buddyID,
		privateKey:   privateKey,
		libp2pHost:   libp2pHost,
		gps:          gps,
		sequencerURL: sequencerURL,
		bftConfig:    config,
		signer:       NewLocalSigner(privateKey),
		tlsCfg:       tlsCfg,
	}
}

// =============================================================================
// Public RPC Handlers
// =============================================================================

// InitiateBFT handles BFT request from the Sequencer.
func (s *BuddyService) InitiateBFT(
	ctx context.Context,
	req *pb.BFTRequest,
) (*pb.BFTResponse, error) {
	if BFTLocal == nil {
		var err error
		BFTLocal, err = common.InitializeGRO(GRO.BFTLocal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize BFT local manager: %w", err)
		}
	}

	log.Printf("🚀 [%s] Received BFT request for round %d", s.buddyID, req.Round)
	log.Printf("   Block: %s", req.BlockHash)
	log.Printf("   GossipSub Topic: %s", req.GossipsubTopic)
	log.Printf("   Buddies: %d", len(req.AllBuddies))

	// Validate request
	if len(req.AllBuddies) < s.bftConfig.MinBuddies {
		return &pb.BFTResponse{
			Accepted: false,
			Message:  fmt.Sprintf("insufficient buddies: %d", len(req.AllBuddies)),
			BuddyId:  s.buddyID,
		}, nil
	}

	// Accept the request
	resp := &pb.BFTResponse{
		Accepted: true,
		Message:  "BFT request accepted, starting consensus",
		BuddyId:  s.buddyID,
	}

	// Run the consensus asynchronously (non-blocking)
	BFTLocal.Go(GRO.BFTConsensusThread, func(ctx context.Context) error {
		s.runBFTConsensus(ctx, req)
		return nil
	})

	return resp, nil
}

// ReportResult receives reports (mainly used by Sequencer acknowledgments)
func (s *BuddyService) ReportResult(
	ctx context.Context,
	result *pb.BFTResult,
) (*pb.ResultAck, error) {
	log.Printf("📥 [%s] Received result report from %s", s.buddyID, result.BuddyId)
	return &pb.ResultAck{
		Received: true,
		Message:  "Result received",
	}, nil
}

// =============================================================================
// Core Logic
// =============================================================================

// runBFTConsensus executes the full BFT consensus flow for a given round.
func (s *BuddyService) runBFTConsensus(ctx context.Context, req *pb.BFTRequest) {
	log.Printf("🔧 [%s] Starting BFT consensus for round %d", s.buddyID, req.Round)

	// Step 1: Create BFT engine
	bftEngine := New(s.bftConfig)

	// Step 2: Create BFT adapter with your existing GossipPubSub
	adapter, err := NewBFTPubSubAdapter(
		ctx,
		s.gps, // Use your existing GossipPubSub instance
		bftEngine,
		req.GossipsubTopic, // channel name
	)
	if err != nil {
		s.reportFailure(req, fmt.Sprintf("failed to create BFT adapter: %v", err))
		return
	}
	defer adapter.Close()

	log.Printf("✅ [%s] BFT adapter initialized on channel: %s", s.buddyID, req.GossipsubTopic)

	// Step 3: Wait for network readiness (optional, but good practice)
	time.Sleep(2 * time.Second)

	// Step 4: Convert protobuf buddies → BFT inputs
	bftInputs := make([]BuddyInput, len(req.AllBuddies))
	for i, buddy := range req.AllBuddies {
		decision := Accept
		if buddy.Decision == "REJECT" {
			decision = Reject
		}
		bftInputs[i] = BuddyInput{
			ID:         buddy.Id,
			Decision:   decision,
			PublicKey:  buddy.PublicKey,
			PrivateKey: nil,
		}
		if buddy.Id == s.buddyID {
			bftInputs[i].PrivateKey = s.privateKey
		}
	}

	// Step 5: Run BFT consensus through the adapter
	result, err := adapter.ProposeConsensus(
		ctx,
		req.Round,
		req.BlockHash,
		s.buddyID,
		bftInputs,
	)
	if err != nil {
		s.reportFailure(req, fmt.Sprintf("consensus failed: %v", err))
		return
	}

	// Step 6: Report result
	log.Printf("✅ [%s] Consensus complete - Decision: %s (Accepted: %v, Duration: %v)",
		s.buddyID, result.Decision, result.BlockAccepted, result.TotalDuration)
	s.reportResult(req, result)
}

// =============================================================================
// Reporting Helpers
// =============================================================================

// reportResult sends the final consensus result to the Sequencer securely.
func (s *BuddyService) reportResult(req *pb.BFTRequest, result *Result) {
	log.Printf("📤 [%s] Reporting result to Sequencer (%s)", s.buddyID, s.sequencerURL)

	var dialOpt grpc.DialOption
	if s.tlsCfg != nil {
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(s.tlsCfg))
	} else {
		log.Printf("⚠️ [%s] TLS config not provided — using insecure dial (NOT for production)", s.buddyID)
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.Dial(s.sequencerURL, dialOpt)
	if err != nil {
		log.Printf("❌ [%s] Failed to dial sequencer: %v", s.buddyID, err)
		return
	}
	defer conn.Close()

	client := pb.NewBFTServiceClient(conn)

	pbResult := &pb.BFTResult{
		Round:         req.Round,
		BlockHash:     req.BlockHash,
		BuddyId:       s.buddyID,
		Success:       result.Success,
		Decision:      string(result.Decision),
		BlockAccepted: result.BlockAccepted,
		PrepareCount:  int32(result.PrepareCount),
		CommitCount:   int32(result.CommitCount),
		Byzantine:     result.ByzantineDetected,
		DurationMs:    result.TotalDuration.Milliseconds(),
		FailureReason: result.FailureReason,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ack, err := client.ReportResult(ctx, pbResult)
	if err != nil {
		log.Printf("❌ [%s] Failed to report result: %v", s.buddyID, err)
		return
	}

	log.Printf("✅ [%s] Result acknowledged by sequencer: %s", s.buddyID, ack.Message)
}

// reportFailure notifies the Sequencer about any consensus failure.
func (s *BuddyService) reportFailure(req *pb.BFTRequest, reason string) {
	log.Printf("📤 [%s] Reporting failure to Sequencer: %s", s.buddyID, reason)

	var dialOpt grpc.DialOption
	if s.tlsCfg != nil {
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(s.tlsCfg))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.Dial(s.sequencerURL, dialOpt)
	if err != nil {
		log.Printf("❌ [%s] Failed to dial sequencer: %v", s.buddyID, err)
		return
	}
	defer conn.Close()

	client := pb.NewBFTServiceClient(conn)
	pbResult := &pb.BFTResult{
		Round:         req.Round,
		BlockHash:     req.BlockHash,
		BuddyId:       s.buddyID,
		Success:       false,
		FailureReason: reason,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = client.ReportResult(ctx, pbResult)
}

// =============================================================================
// GRPC Server Setup
// =============================================================================

// StartServer starts the BuddyService gRPC server with optional mTLS.
func (s *BuddyService) StartServer(port int) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	var grpcServer *grpc.Server
	if s.tlsCfg != nil {
		grpcServer = grpc.NewServer(grpc.Creds(credentials.NewTLS(s.tlsCfg)))
		log.Printf("🔐 [%s] BFT gRPC server using mTLS", s.buddyID)
	} else {
		grpcServer = grpc.NewServer()
		log.Printf("⚠️ [%s] BFT gRPC server started WITHOUT TLS (development only)", s.buddyID)
	}

	pb.RegisterBFTServiceServer(grpcServer, s)

	log.Printf("🎧 [%s] Listening on port %d", s.buddyID, port)
	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve: %w", err)
	}

	return nil
}

// =============================================================================
// Utility
// =============================================================================

// GetID returns the BuddyService's ID (exported for integration and logging).
func (s *BuddyService) GetID() string {
	return s.buddyID
}
