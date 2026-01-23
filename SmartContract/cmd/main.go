package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/SmartContract/internal/state"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gossipnode/SmartContract/internal/storage"
	pb "gossipnode/gETH/proto"
)

func main() {
	// Configure logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	port := 15055
	chainID := 7000700

	fmt.Printf("🚀 Starting SmartContract gRPC server\n")
	fmt.Printf("   Port: %d\n", port)
	fmt.Printf("   Chain ID: %d\n", chainID)

	// 1. Initialize Database Configuration
	dbConfig := database.LoadConfigFromEnv()
	fmt.Printf("   DB Type: %s\n", dbConfig.Type)

	// 2. Initialize Shared KVStore (Pebble/Memory)
	fmt.Printf("   Initializing Shared Storage (%s)...\n", dbConfig.Type)
	storeConfig := storage.ConfigFromEnv(dbConfig)
	kvStore, err := storage.NewKVStore(storeConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize KVStore")
	}
	defer kvStore.Close()

	// 3. Initialize Registry (Layer 2)
	fmt.Println("   Initializing Registry...")
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry factory")
	}

	// Inject shared store into Registry
	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry")
	}
	defer reg.Close()

	// 4. Initialize ContractDB (State Layer)
	fmt.Printf("   Initializing ContractDB...\n")

	// ... gETH client initialization ...
	gethURL := "localhost:9090"
	conn, err := grpc.Dial(gethURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to gETH node")
	}
	defer conn.Close()

	chainClient := pb.NewChainClient(conn)

	stateDB := state.NewContractDB(chainClient, kvStore)

	// 4. Initialize Router (Layer 3)
	fmt.Println("   Initializing Router...")

	// Router expects vm.StateDB interface likely?
	// router.NewRouter signature needs checking if it took specific *ImmuStateDB or interface.
	// Assuming it takes StateDB interface.

	smartRouter := router.NewRouter(chainID, stateDB, reg, nil)
	defer smartRouter.Close()

	// 5. Start Server
	fmt.Printf("✅ Server ready on localhost:%d\n\n", port)

	// Create context that cancels on interrupt
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown gracefully
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		fmt.Println("\n⚠️  Shutting down...")
		cancel()
	}()

	if err := router.StartGRPC(port, smartRouter); err != nil {
		log.Fatal().Err(err).Msg("Server failed")
	}
}
