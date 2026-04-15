package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gossipnode/logging"

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
	ctx := context.Background()

	cfg, err := settings.Load()
	if err != nil {
		logger().Warn(ctx, "Failed to load jmdn.yaml — using defaults", ion.Err(err))
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
		logger().Error(ctx, "Failed to initialize KVStore", err)
		os.Exit(1)
	}
	contractDB.SetSharedKVStore(kvStore)

	// 3. DB_OPs connection pools (for nonce / account lookups)
	poolConfig := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPool(poolConfig); err != nil {
		logger().Warn(ctx, "Failed to initialize DB_OPs pool — nonce retrieval might fail", ion.Err(err))
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		logger().Warn(ctx, "Failed to initialize Accounts pool — DID checks might fail", ion.Err(err))
	}

	// 4. Contract registry
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		logger().Error(ctx, "Failed to create registry factory", err)
		os.Exit(1)
	}
	Security.SetExpectedChainID(chainID)
	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		logger().Error(ctx, "Failed to create registry", err)
		os.Exit(1)
	}

	// 5. gETH gRPC client
	gethConn, err := grpc.NewClient(gethAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger().Error(ctx, "Failed to connect to gETH node", err,
			ion.String("addr", gethAddr))
		os.Exit(1)
	}
	defer gethConn.Close()
	chainClient := pb.NewChainClient(gethConn)

	// 6. DID gRPC client
	didConn, err := grpc.NewClient(didAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger().Error(ctx, "Failed to connect to DID service", err,
			ion.String("addr", didAddr))
		os.Exit(1)
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

	ctxWithCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		fmt.Println("\n⚠️  Shutting down...")
		cancel()
	}()

	if err := router.StartGRPC(ctxWithCancel, port, smartRouter); err != nil {
		logger().Error(ctx, "Server failed", err)
		os.Exit(1)
	}
}

// logger returns the named ion logger for the main package.
func logger() *ion.Ion {
	logInstance, err := logging.NewAsyncLogger().Get().NamedLogger(logging.SmartContract, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
