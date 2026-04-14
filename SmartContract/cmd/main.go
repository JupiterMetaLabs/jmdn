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
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/repository"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/SmartContract/internal/storage"
	"gossipnode/config"
	"gossipnode/config/settings"
	pb "gossipnode/gETH/proto"
)

func main() {
	// Configure logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Load unified node config (jmdn.yaml → env vars → defaults).
	// This gives us the same port/bind values as the integrated server.
	cfg, err := settings.Load()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load jmdn.yaml — using defaults")
		defaultCfg := settings.DefaultConfig()
		cfg = &defaultCfg
	}

	port := cfg.Ports.Smart
	chainID := cfg.Network.ChainID
	gethAddr := fmt.Sprintf("localhost:%d", cfg.Ports.Geth)
	didAddr := fmt.Sprintf("%s:%d", cfg.Binds.DID, cfg.Ports.DID)

	fmt.Printf("🚀 Starting SmartContract gRPC server\n")
	fmt.Printf("   Port     : %d\n", port)
	fmt.Printf("   Chain ID : %d\n", chainID)
	fmt.Printf("   gETH     : %s\n", gethAddr)
	fmt.Printf("   DID      : %s\n", didAddr)

	// 1. Initialize Database Configuration
	dbConfig := database.LoadConfigFromEnv()
	fmt.Printf("   DB Type  : %s\n", dbConfig.Type)

	// 2. Initialize Shared KVStore (Pebble/Memory)
	storeConfig := storage.ConfigFromEnv(dbConfig)
	kvStore, err := storage.NewKVStore(storeConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize KVStore")
	}
	evm.SetSharedKVStore(kvStore)

	// 3. Initialize DB_OPs pools (for nonce/account lookups)
	poolConfig := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPool(poolConfig); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize DB_OPs pool — nonce retrieval might fail")
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize Accounts pool — DID checks might fail")
	}

	// 4. Initialize Registry
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry factory")
	}
	Security.SetExpectedChainID(chainID)
	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry")
	}

	// 5. gETH gRPC client
	gethConn, err := grpc.NewClient(gethAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Str("addr", gethAddr).Msg("Failed to connect to gETH node")
	}
	defer gethConn.Close()
	chainClient := pb.NewChainClient(gethConn)

	// 6. DID gRPC client
	didConn, err := grpc.NewClient(didAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Str("addr", didAddr).Msg("Failed to connect to DID service")
	}
	defer didConn.Close()
	didClient := pbdid.NewDIDServiceClient(didConn)

	// Share DID client with EVM package so InitializeStateDB reuses this connection.
	evm.SetSharedDIDClient(didClient)

	// 7. Initialize ContractDB (StateDB)
	repo := repository.NewPebbleAdapter(kvStore)
	stateDB := state.NewContractDB(didClient, repo)

	// 8. Initialize Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)
	defer smartRouter.Close()

	fmt.Printf("✅ Server ready on localhost:%d\n\n", port)

	// Create context that cancels on interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
