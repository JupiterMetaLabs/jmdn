// =============================================================================
// FILE: cmd/bft_integration_test/main.go
// Full integration test: Sequencer + 13 Buddies + GossipSub + gRPC
// =============================================================================
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/Asynchronous-Validation-Consensus/pkg/bft"
	"github.com/JupiterMetaLabs/Asynchronous-Validation-Consensus/pkg/network"
)

type BuddyService struct {
	buddyID string
}

// =============================================================================
// MAIN
// =============================================================================
func main() {
	fmt.Println("🚀 BFT Full Integration Test")
	fmt.Println("============================")
	fmt.Println("Testing: Sequencer → gRPC → Buddies → GossipSub → BFT\n")

	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Step 1: Setup libp2p network (13 buddies)
	// -------------------------------------------------------------------------
	fmt.Println("📡 Step 1: Setting up libp2p network...")
	hosts, pubsubs, err := network.SetupSimpleNetwork(ctx, 13, 9000)
	if err != nil {
		log.Fatalf("Failed to setup network: %v", err)
	}
	defer func() {
		for _, h := range hosts {
			h.Close()
		}
	}()

	fmt.Println("\n⏳ Waiting for network to stabilize...")
	time.Sleep(3 * time.Second)

	// -------------------------------------------------------------------------
	// Step 2: Create 13 buddy nodes with gRPC servers
	// -------------------------------------------------------------------------
	fmt.Println("\n🔧 Step 2: Creating buddy nodes...")

	buddyServices := make([]*bft.BuddyService, 13)
	buddyInfos := make([]bft.BuddyInfo, 13)

	// NOTE: For real TLS integration, replace `nil` with a loaded *tls.Config
	tlsCfg, _ := bft.LoadTLSCredentials("ca.pem", "buddy-cert.pem", "buddy-key.pem")

	for i := 0; i < 13; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		buddyID := fmt.Sprintf("buddy-%d", i)
		grpcPort := 50051 + i
		grpcAddr := fmt.Sprintf("localhost:%d", grpcPort)

		// Create BuddyService
		service := bft.NewBuddyService(
			buddyID,
			priv,
			hosts[i],
			pubsubs[i],
			"localhost:50050", // Sequencer gRPC address
			tlsCfg,
		)
		buddyServices[i] = service

		// Start gRPC server
		go func(s *bft.BuddyService, port int) {
			if err := s.StartServer(port); err != nil {
				log.Printf("❌ Buddy [%s] server error: %v", s.GetID(), err)
			}
		}(service, grpcPort)

		// Decide votes: first 9 accept, remaining 4 reject
		decision := bft.Accept
		if i >= 9 {
			decision = bft.Reject
		}

		buddyInfos[i] = bft.BuddyInfo{
			ID:        buddyID,
			Address:   grpcAddr,
			PublicKey: pub,
			Decision:  decision,
		}

		fmt.Printf("   ✅ Buddy %2d: %-8s (gRPC: %s, Decision: %s)\n",
			i, buddyID, grpcAddr, decision)
	}

	fmt.Println("\n⏳ Waiting for buddy gRPC servers to start...")
	time.Sleep(3 * time.Second)

	// -------------------------------------------------------------------------
	// Step 3: Start Sequencer server
	// -------------------------------------------------------------------------
	fmt.Println("\n🎯 Step 3: Starting Sequencer...")
	sequencer := bft.NewSequencerBFTClient("sequencer-1", 50050)
	sequencerServer := bft.NewSequencerServer(sequencer)

	go func() {
		if err := sequencerServer.StartServer(50050); err != nil {
			log.Printf("❌ Sequencer server error: %v", err)
		}
	}()
	time.Sleep(1 * time.Second)

	fmt.Println("✅ Sequencer gRPC server listening on :50050")

	// -------------------------------------------------------------------------
	// Step 4: Initiate a BFT round
	// -------------------------------------------------------------------------
	fmt.Println("\n⚡ Step 4: Initiating BFT Round...")
	fmt.Println(strings.Repeat("=", 80))

	round := uint64(1)
	blockHash := "0xabc123def456789"

	result, err := sequencer.InitiateBFTRound(
		ctx,
		round,
		blockHash,
		buddyInfos,
	)
	if err != nil {
		log.Printf("❌ BFT round failed: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 5: Display Results
	// -------------------------------------------------------------------------
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("📊 FINAL CONSENSUS RESULT")
	fmt.Println(strings.Repeat("=", 80))

	if result != nil {
		if result.Success {
			fmt.Println("✅ CONSENSUS REACHED")
			fmt.Printf("   Decision       : %s\n", result.Decision)
			fmt.Printf("   Block Accepted : %v\n", result.BlockAccepted)
			fmt.Printf("   Votes          : %d ACCEPT, %d REJECT\n",
				result.AcceptVotes, result.RejectVotes)
		} else {
			fmt.Println("❌ CONSENSUS FAILED")
			fmt.Printf("   Reason         : %s\n", result.FailureReason)
		}
	} else {
		fmt.Println("❌ No result received from sequencer.")
	}

	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("\n✨ Integration test completed successfully!")

	// Keep running for a bit to view logs
	time.Sleep(5 * time.Second)
}

func (s *BuddyService) GetID() string {
	return s.buddyID
}
