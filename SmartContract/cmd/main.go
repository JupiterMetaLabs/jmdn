package main

import (
	"fmt"

	router "gossipnode/SmartContract/Router"
	"gossipnode/SmartContract/statedb"

	"github.com/rs/zerolog/log"
)

func main() {
	port := 15055
	chainID := 7000700

	fmt.Printf("🚀 Starting SmartContract gRPC server\n")
	fmt.Printf("   Port: %d\n", port)
	fmt.Printf("   Chain ID: %d\n", chainID)
	fmt.Printf("   State: In-Memory\n\n")

	// Create in-memory state database (no external dependencies!)
	stateDB := statedb.NewInMemoryStateDB()

	// Start the gRPC server using your Router architecture
	fmt.Printf("✅ Starting server on localhost:%d\n\n", port)
	if err := router.StartGRPC(port, chainID, stateDB); err != nil {
		log.Fatal().Err(err).Msg("Server failed")
	}
}
