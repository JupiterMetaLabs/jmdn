// =============================================================================
// FILE: pkg/bft/buddy_service.go
// =============================================================================
package bft

import (
	"context"
	"crypto/tls"
	"fmt"
	pb "gossipnode/AVC/BFT/proto"
	"log"
	"net"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
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
	pubsub       *pubsub.PubSub
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
	ps *pubsub.PubSub,
	sequencerURL string,
	tlsCfg *tls.Config,
) *BuddyService {

	config := DefaultConfig()
	// Extend timeouts slightly for real network latency
	config.PrepareTimeout = 10 * time.Second
	config.CommitTimeout = 10 * time.Second

	return &BuddyService{
		buddyID:      buddyID,
		privateKey:   privateKey,
		libp2pHost:   libp2pHost,
		pubsub:       ps,
		sequencerURL: sequencerURL,
		bftConfig:    config,
		signer:       NewLocalSigner(privateKey), // wrapped signer abstraction
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
	go s.runBFTConsensus(context.Background(), req)

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

	// Step 1: Join GossipSub topic
	messenger, err := NewGossipSubMessenger(ctx, s.libp2pHost, s.pubsub, req.GossipsubTopic)
	if err != nil {
		s.reportFailure(req, fmt.Sprintf("failed to join gossipsub: %v", err))
		return
	}
	defer messenger.Stop()

	// Start listening
	if err := messenger.Start(); err != nil {
		s.reportFailure(req, fmt.Sprintf("failed to start messenger: %v", err))
		return
	}

	log.Printf("✅ [%s] Joined GossipSub topic: %s", s.buddyID, req.GossipsubTopic)

	// Step 2: Wait for mesh readiness
	if err := waitForMesh(ctx, messenger, len(req.AllBuddies)); err != nil {
		s.reportFailure(req, fmt.Sprintf("mesh readiness failed: %v", err))
		return
	}

	// Step 3: Convert protobuf buddies → BFT inputs
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
		// Register public keys for signature validation (used by messenger)
		if len(buddy.PublicKey) > 0 {
			RegisterPublicKey(buddy.Id, buddy.PublicKey)
		}
	}

	// Step 4: Run BFT consensus
	engine := New(s.bftConfig)
	result, err := engine.RunConsensus(
		ctx,
		req.Round,
		req.BlockHash,
		s.buddyID,
		bftInputs,
		messenger,
		s.signer, // Pass signer explicitly (no setSigner needed)
	)
	if err != nil {
		s.reportFailure(req, fmt.Sprintf("consensus failed: %v", err))
		return
	}

	// Step 5: Report result
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
// Mesh readiness
// =============================================================================

// waitForMesh waits until the GossipSub mesh has expected peers or times out.
func waitForMesh(ctx context.Context, messenger *GossipSubMessenger, expectedPeers int) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("mesh readiness timed out after 10s")
		case <-ticker.C:
			peers := messenger.topic.ListPeers()
			if len(peers) >= expectedPeers-1 {
				log.Printf("🌐 Mesh ready with %d peers (expected %d)", len(peers), expectedPeers)
				return nil
			}
		}
	}
}

// GetID returns the BuddyService's ID (exported for integration and logging).
func (s *BuddyService) GetID() string {
	return s.buddyID
}
