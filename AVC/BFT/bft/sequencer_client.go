// =============================================================================
// FILE: pkg/bft/sequencer_client.go
// =============================================================================
package bft

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "gossipnode/AVC/BFT/proto"
	"gossipnode/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"
)

// SequencerBFTClient handles BFT initiation from Sequencer side
type SequencerBFTClient struct {
	sequencerID string
	grpcPort    int

	mu      sync.RWMutex
	results map[string]*pb.BFTResult // buddyID -> result

	// Security
	tlsCreds credentials.TransportCredentials
}

// NewSequencerBFTClient creates a new Sequencer BFT client
func NewSequencerBFTClient(sequencerID string, grpcPort int) (*SequencerBFTClient, error) {
	// 1. Setup TLS Loader
	secCfg := &settings.Get().Security
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger())

	// 2. Load Client Credentials (Standardized Helper)
	// We are "sequencer_client" connecting to BFT Buddy servers
	creds, err := tlsLoader.LoadClientCredentials(settings.ServiceBFTBuddy, "sequencer_client")
	if err != nil {
		log.Printf("❌ Failed to load TLS credentials for Sequencer Client: %v", err)
		return nil, fmt.Errorf("failed to load TLS credentials for Sequencer Client: %w", err)
	}

	log.Printf("🔒 Sequencer Client Credentials Loaded")

	return &SequencerBFTClient{
		sequencerID: sequencerID,
		grpcPort:    grpcPort,
		results:     make(map[string]*pb.BFTResult),
		tlsCreds:    creds,
	}, nil
}

// InitiateBFTRound initiates BFT consensus for a round
func (s *SequencerBFTClient) InitiateBFTRound(
	ctx context.Context,
	round uint64,
	blockHash string,
	buddies []BuddyInfo,
) (*ConsensusResult, error) {

	log.Printf("🚀 [Sequencer] Initiating BFT round %d", round)
	log.Printf("   Block: %s", blockHash)
	log.Printf("   Buddies: %d", len(buddies))

	// Create GossipSub topic name
	topicName := fmt.Sprintf("bft-round-%d", round)

	// Convert buddies to protobuf format
	pbBuddies := make([]*pb.BuddyInfo, len(buddies))
	for i, buddy := range buddies {
		pbBuddies[i] = &pb.BuddyInfo{
			Id:        buddy.ID,
			Address:   buddy.Address,
			PublicKey: buddy.PublicKey,
			Decision:  string(buddy.Decision),
		}
	}

	// Create BFT request
	request := &pb.BFTRequest{
		Round:          round,
		BlockHash:      blockHash,
		GossipsubTopic: topicName,
		AllBuddies:     pbBuddies,
		TimeoutSeconds: 30,
	}

	// Clear previous results
	s.mu.Lock()
	s.results = make(map[string]*pb.BFTResult)
	s.mu.Unlock()

	// Send request to all buddies in parallel
	wg, err := BFTLocal.NewFunctionWaitGroup(context.Background(), GRO.BFTWaitGroup)
	if err != nil {
		return nil, fmt.Errorf("failed to create wait group: %w", err)
	}

	responses := make(chan *BuddyResponse, len(buddies))

	for _, buddy := range buddies {
		buddyForGoroutine := buddy
		if err := BFTLocal.Go(GRO.BFTSendRequestThread, func(ctx context.Context) error {
			response := s.sendBFTRequest(ctx, buddyForGoroutine, request)
			responses <- response
			return nil
		}, local.AddToWaitGroup(GRO.BFTWaitGroup)); err != nil {
			return nil, fmt.Errorf("failed to start goroutine: %w", err)
		}
	}

	// Wait for all requests to complete
	wg.Wait()
	close(responses)

	// Check responses
	acceptedCount := 0
	for resp := range responses {
		if resp.Accepted {
			acceptedCount++
			log.Printf("✅ [Sequencer] Buddy %s accepted BFT request", resp.BuddyID)
		} else {
			log.Printf("❌ [Sequencer] Buddy %s rejected: %s", resp.BuddyID, resp.Message)
		}
	}

	expectedCount := len(buddies)
	threshold := QuorumThreshold(expectedCount)
	if acceptedCount < threshold {
		return nil, fmt.Errorf("insufficient buddies accepted: %d/%d (need %d)", acceptedCount, expectedCount, threshold)
	}

	log.Printf("✅ [Sequencer] %d buddies accepted, waiting for results...", acceptedCount)

	// Wait for results (with timeout)
	result := s.waitForResults(ctx, round, len(buddies), 60*time.Second)

	return result, nil
}

// sendBFTRequest sends BFT request to a single buddy
func (s *SequencerBFTClient) sendBFTRequest(
	ctx context.Context,
	buddy BuddyInfo,
	request *pb.BFTRequest,
) *BuddyResponse {

	// Connect to buddy
	// Connect to buddy using stored credentials
	// Note: dialing inside the loop is inefficient, connection pooling is better
	// but keeping existing architecture for now.
	conn, err := grpc.Dial(buddy.Address, grpc.WithTransportCredentials(s.tlsCreds))
	if err != nil {
		log.Printf("❌ [Sequencer] Failed to connect to %s: %v", buddy.ID, err)
		return &BuddyResponse{
			BuddyID:  buddy.ID,
			Accepted: false,
			Message:  fmt.Sprintf("connection failed: %v", err),
		}
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("❌ [Sequencer] Failed to close connection to %s: %v", buddy.ID, err)
		}
	}()

	client := pb.NewBFTServiceClient(conn)

	// Send request with timeout
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	response, err := client.InitiateBFT(callCtx, request)
	if err != nil {
		log.Printf("❌ [Sequencer] RPC failed to %s: %v", buddy.ID, err)
		return &BuddyResponse{
			BuddyID:  buddy.ID,
			Accepted: false,
			Message:  fmt.Sprintf("rpc failed: %v", err),
		}
	}

	return &BuddyResponse{
		BuddyID:  response.BuddyId,
		Accepted: response.Accepted,
		Message:  response.Message,
	}
}

// waitForResults waits for BFT results from buddies
func (s *SequencerBFTClient) waitForResults(
	ctx context.Context,
	round uint64,
	expectedCount int,
	timeout time.Duration,
) *ConsensusResult {

	log.Printf("⏳ [Sequencer] Waiting for results (timeout: %v)", timeout)

	deadline := time.Now().UTC().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.finalizeResults(round, "context cancelled")

		case <-ticker.C:
			s.mu.RLock()
			count := len(s.results)
			s.mu.RUnlock()

			log.Printf("📊 [Sequencer] Results received: %d/%d", count, expectedCount)

			// Check if we have enough results (quorum threshold)
			threshold := QuorumThreshold(expectedCount)
			if count >= threshold {
				return s.finalizeResults(round, "")
			}

			// Check timeout
			if time.Now().UTC().After(deadline) {
				return s.finalizeResults(round, "timeout waiting for results")
			}
		}
	}
}

// finalizeResults computes final consensus decision
func (s *SequencerBFTClient) finalizeResults(round uint64, failureReason string) *ConsensusResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	log.Printf("📊 [Sequencer] Finalizing results for round %d", round)

	if len(s.results) == 0 {
		return &ConsensusResult{
			Round:         round,
			Success:       false,
			FailureReason: "no results received",
		}
	}

	// Count votes
	acceptCount := 0
	rejectCount := 0
	successCount := 0

	for buddyID, result := range s.results {
		if result.Success {
			successCount++
			if result.Decision == "ACCEPT" {
				acceptCount++
			} else {
				rejectCount++
			}
		}
		log.Printf("   %s: Success=%v, Decision=%s", buddyID, result.Success, result.Decision)
	}

	// Determine consensus using quorum threshold
	totalBuddies := len(s.results)
	threshold := QuorumThreshold(totalBuddies)
	consensusReached := HasQuorum(successCount, totalBuddies)
	var finalDecision Decision
	blockAccepted := false

	if HasQuorum(acceptCount, totalBuddies) {
		finalDecision = Accept
		blockAccepted = true
	} else if HasQuorum(rejectCount, totalBuddies) {
		finalDecision = Reject
		blockAccepted = false
	} else {
		finalDecision = ""
		consensusReached = false
	}

	result := &ConsensusResult{
		Round:         round,
		Success:       consensusReached,
		Decision:      finalDecision,
		BlockAccepted: blockAccepted,
		TotalBuddies:  len(s.results),
		AcceptVotes:   acceptCount,
		RejectVotes:   rejectCount,
		FailureReason: failureReason,
	}

	if consensusReached {
		log.Printf("✅ [Sequencer] CONSENSUS REACHED!")
		log.Printf("   Decision: %s", finalDecision)
		log.Printf("   Block Accepted: %v", blockAccepted)
		log.Printf("   Votes: %d ACCEPT, %d REJECT", acceptCount, rejectCount)
	} else {
		log.Printf("❌ [Sequencer] CONSENSUS FAILED")
		log.Printf("   Reason: %s", failureReason)
		log.Printf("   Results: %d/%d (threshold: %d)", len(s.results), totalBuddies, threshold)
	}

	return result
}

// RecordResult stores a result from a buddy (called by gRPC handler)
func (s *SequencerBFTClient) RecordResult(result *pb.BFTResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.results[result.BuddyId] = result
	log.Printf("📥 [Sequencer] Recorded result from %s: %s", result.BuddyId, result.Decision)
}

// ===========================================================================
// Helper Types
// ===========================================================================

type BuddyInfo struct {
	ID        string
	Address   string // gRPC address (e.g., "localhost:50051")
	PublicKey []byte
	Decision  Decision
}

type BuddyResponse struct {
	BuddyID  string
	Accepted bool
	Message  string
}

type ConsensusResult struct {
	Round         uint64
	Success       bool
	Decision      Decision
	BlockAccepted bool
	TotalBuddies  int
	AcceptVotes   int
	RejectVotes   int
	FailureReason string
}
