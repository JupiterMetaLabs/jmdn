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

	contractDB "gossipnode/DB_OPs/contractDB"
	"gossipnode/DB_OPs"
	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/config"
	"gossipnode/config/settings"
	pb "gossipnode/gETH/proto"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

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

	// 1. Database config (used by contract registry)
	dbConfig := database.LoadConfigFromEnv()
	fmt.Printf("   DB Type  : %s\n", dbConfig.Type)

	// 2. Shared KVStore
	kvStore, err := contractDB.NewKVStore(contractDB.DefaultConfig())
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize KVStore")
	}
	contractDB.SetSharedKVStore(kvStore)

	// 3. DB_OPs connection pools (for nonce / account lookups)
	poolConfig := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPool(poolConfig); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize DB_OPs pool — nonce retrieval might fail")
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize Accounts pool — DID checks might fail")
	}

	// 4. Contract registry
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
	contractDB.SetSharedDIDClient(didClient)

	// 7. ContractDB (StateDB)
	repo := contractDB.NewPebbleAdapter(kvStore)
	stateDB := contractDB.NewContractDB(didClient, repo)

	// 8. Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)
	defer smartRouter.Close()

	fmt.Printf("✅ Server ready on localhost:%d\n\n", port)

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
