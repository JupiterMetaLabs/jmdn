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

	"google.golang.org/grpc"
)

// SequencerBFTClient handles BFT initiation from Sequencer side
type SequencerBFTClient struct {
	sequencerID string
	grpcPort    int

	mu      sync.RWMutex
	results map[string]*pb.BFTResult // buddyID -> result
}

// NewSequencerBFTClient creates a new Sequencer BFT client
func NewSequencerBFTClient(sequencerID string, grpcPort int) *SequencerBFTClient {
	return &SequencerBFTClient{
		sequencerID: sequencerID,
		grpcPort:    grpcPort,
		results:     make(map[string]*pb.BFTResult),
	}
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
	var wg sync.WaitGroup
	responses := make(chan *BuddyResponse, len(buddies))

	for _, buddy := range buddies {
		wg.Add(1)
		go func(b BuddyInfo) {
			defer wg.Done()
			response := s.sendBFTRequest(ctx, b, request)
			responses <- response
		}(buddy)
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

	if acceptedCount < 9 {
		return nil, fmt.Errorf("insufficient buddies accepted: %d/13", acceptedCount)
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
	conn, err := grpc.Dial(buddy.Address, grpc.WithInsecure())
	if err != nil {
		log.Printf("❌ [Sequencer] Failed to connect to %s: %v", buddy.ID, err)
		return &BuddyResponse{
			BuddyID:  buddy.ID,
			Accepted: false,
			Message:  fmt.Sprintf("connection failed: %v", err),
		}
	}
	defer conn.Close()

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

	deadline := time.Now().Add(timeout)
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

			// Check if we have enough results (9/13 threshold)
			if count >= 9 {
				return s.finalizeResults(round, "")
			}

			// Check timeout
			if time.Now().After(deadline) {
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

	// Determine consensus
	consensusReached := successCount >= 9
	var finalDecision Decision
	blockAccepted := false

	if acceptCount >= 9 {
		finalDecision = Accept
		blockAccepted = true
	} else if rejectCount >= 9 {
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
		log.Printf("   Results: %d/%d", len(s.results), 13)
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
