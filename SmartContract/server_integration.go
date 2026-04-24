package SmartContract

import (
	"context"
	"fmt"
	"strings"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gossipnode/DB_OPs/cassata"
	contractDB "gossipnode/DB_OPs/contractDB"
	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/config/settings"
	pb "gossipnode/gETH/proto"
)

// StartIntegratedServer initialises and starts the Smart Contract gRPC server
// within the context of the main JMDN node, sharing the process-wide DB lock.
func StartIntegratedServer(ctx context.Context, port int, chainID int, gethPort int, didAddr string, blockgenPort int, cas *cassata.Cassata) error {
	logger().Info(ctx, "Initializing Smart Contract Service...")

	if blockgenPort > 0 {
		evmEndpoint := fmt.Sprintf("http://localhost:%d", blockgenPort)
		evm.SetAPIEndpoint(evmEndpoint)
		logger().Info(ctx, "Configured EVM Block API endpoint",
			ion.String("endpoint", evmEndpoint))
	}

	// 1. Shared KVStore (for contract registry persistence)
	dbConfig := database.LoadConfigFromEnv()
	kvStore, err := contractDB.NewKVStore(contractDB.DefaultConfig())
	if err != nil {
		return fmt.Errorf("failed to initialize KVStore for Smart Contracts: %w", err)
	}

	// 2. Contract Registry
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		return fmt.Errorf("failed to create registry factory: %w", err)
	}

	Security.SetExpectedChainID(chainID)

	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		return fmt.Errorf("failed to create registry: %w", err)
	}

	// 3. gETH gRPC client
	gethClientConn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", gethPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger().Warn(context.Background(), "Failed to create gETH client connection", ion.Err(err))
	}
	if gethClientConn != nil {
		defer func() {
			if closeErr := gethClientConn.Close(); closeErr != nil {
				logger().Warn(context.Background(), "Failed to close gETH client connection", ion.Err(closeErr))
			}
		}()
	}
	chainClient := pb.NewChainClient(gethClientConn)

	// 4. DID gRPC client
	didClientConn, err := grpc.NewClient(
		didAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger().Warn(context.Background(), "Failed to create DID client connection", ion.Err(err))
	}
	if didClientConn != nil {
		defer func() {
			if closeErr := didClientConn.Close(); closeErr != nil {
				logger().Warn(context.Background(), "Failed to close DID client connection", ion.Err(closeErr))
			}
		}()
	}
	didClient := pbdid.NewDIDServiceClient(didClientConn)

	// Share DID client with contractDB so InitializeStateDB never dials a new connection.
	contractDB.SetSharedDIDClient(didClient)
	logger().Info(ctx, "Shared DID client registered.",
		ion.String("did_addr", didAddr))

	// Share the contract registry so gossip receivers can persist contract metadata.
	SetSharedRegistry(reg)
	logger().Info(ctx, "Shared contract registry registered.")

	// 5. ContractDB (State Layer)
	cfg := settings.Get()
	useThebeRepo := cas != nil && !strings.EqualFold(strings.TrimSpace(cfg.ContractDB.Backend), "pebble")

	var repo contractDB.StateRepository
	if useThebeRepo {
		repo = contractDB.NewThebeStateRepository(cas)
		logger().Info(ctx, "ContractDB: using ThebeStateRepository (ThebeDB-backed)")
	} else {
		contractDB.SetSharedKVStore(kvStore)
		repo = contractDB.NewPebbleAdapter(kvStore)
		logger().Warn(ctx, "ContractDB: falling back to PebbleAdapter — set contractdb.backend=thebe for production")
	}

	stateDB := contractDB.NewContractDB(didClient, repo)

	// 6. Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)

	// 7. Start gRPC server (blocks until ctx is cancelled)
	logger().Info(ctx, "Starting Integrated Smart Contract gRPC server",
		ion.Int("port", port))

	if err := router.StartGRPC(ctx, port, smartRouter); err != nil {
		return fmt.Errorf("smart contract gRPC server failed: %w", err)
	}

	return nil
}
