package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gossipnode/DB_OPs"
	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/SmartContract/internal/storage"
	"gossipnode/config"
	pb "gossipnode/gETH/proto"
)

func main() {
	// Configure logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	port := 15056
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
	// Note: smartRouter.Close() will handle closing kvStore via registry

	// 2.5 Initialize DB_OPs (Main DB Connection for Nonce/History)
	// We need this to fetch nonces via DB_OPs.CountTransactionsByAccount
	fmt.Println("   Initializing DB_OPs Pool...")
	poolConfig := config.DefaultConnectionPoolConfig()
	// Assuming default immudb/immudb credentials for local dev
	if err := DB_OPs.InitMainDBPoolWithLoki(poolConfig, false, dbConfig.Username, dbConfig.Password); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize DB_OPs pool - Nonce retrieval might fail")
	}

	fmt.Println("   Initializing Accounts Pool...")
	if err := DB_OPs.InitAccountsPoolWithLoki(false, dbConfig.Username, dbConfig.Password); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize Accounts pool - DID checks might fail")
	}

	// 3. Initialize Registry (Layer 2)
	fmt.Println("   Initializing Registry...")
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry factory")
	}

	// Set chain ID for security checks
	Security.SetExpectedChainID(chainID)

	// Inject shared store into Registry
	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry")
	}
	// Note: smartRouter.Close() will handle closing reg

	// 4. Initialize ContractDB (State Layer)
	fmt.Printf("   Initializing ContractDB...\n")

	// ... gETH client initialization ...
	// ... gETH client initialization (Code/Storage) ...
	gethURL := "localhost:15054"
	conn, err := grpc.Dial(gethURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to gETH node")
	}
	defer conn.Close()
	chainClient := pb.NewChainClient(conn)

	// ... DID client initialization (Balance/Nonce) ...
	didURL := "localhost:15052"
	didConn, err := grpc.Dial(didURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to DID Service")
	}
	defer didConn.Close()
	didClient := pbdid.NewDIDServiceClient(didConn)

	stateDB := state.NewContractDB(chainClient, didClient, kvStore)

	// 4. Initialize Router (Layer 3)
	fmt.Println("   Initializing Router...")

	// Router expects vm.StateDB interface likely?
	// router.NewRouter signature needs checking if it took specific *ImmuStateDB or interface.
	// Assuming it takes StateDB interface.

	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)
	defer smartRouter.Close()

	// 5. Start Server
	fmt.Printf("✅ Server ready on localhost:%d\n\n", port)

	// Create context that cancels on interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown gracefully
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		fmt.Println("\n⚠️  Shutting down...")
		cancel()
	}()

	if err := router.StartGRPC(ctx, port, smartRouter); err != nil {
		log.Fatal().Err(err).Msg("Server failed")
	}
}
