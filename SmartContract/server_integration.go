package SmartContract

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	contractDB "gossipnode/DB_OPs/contractDB"
	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/router"
	pb "gossipnode/gETH/proto"
)

// StartIntegratedServer initialises and starts the Smart Contract gRPC server
// within the context of the main JMDN node, sharing the process-wide DB lock.
func StartIntegratedServer(ctx context.Context, port int, chainID int, gethPort int, didAddr string, blockgenPort int) error {
	log.Info().Msg("Initializing Smart Contract Service...")

	if blockgenPort > 0 {
		evmEndpoint := fmt.Sprintf("http://localhost:%d", blockgenPort)
		evm.SetAPIEndpoint(evmEndpoint)
		log.Info().Str("endpoint", evmEndpoint).Msg("Configured EVM Block API endpoint")
	}

	// 1. Shared KVStore (Pebble singleton — must be opened once per process)
	dbConfig := database.LoadConfigFromEnv()
	kvStore, err := contractDB.NewKVStore(contractDB.DefaultConfig())
	if err != nil {
		return fmt.Errorf("failed to initialize KVStore for Smart Contracts: %w", err)
	}

	// Share with contractDB package so all EVM executions reuse this handle.
	contractDB.SetSharedKVStore(kvStore)
	log.Info().Msg("Shared KVStore for contract storage initialised.")

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
		log.Warn().Err(err).Msg("Failed to create gETH client connection (SmartContract)")
	}
	chainClient := pb.NewChainClient(gethClientConn)

	// 4. DID gRPC client
	didClientConn, err := grpc.NewClient(
		didAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create DID client connection (SmartContract)")
	}
	didClient := pbdid.NewDIDServiceClient(didClientConn)

	// Share DID client with contractDB so InitializeStateDB never dials a new connection.
	contractDB.SetSharedDIDClient(didClient)
	log.Info().Str("did_addr", didAddr).Msg("Shared DID client registered.")

	// 5. ContractDB (State Layer)
	repo := contractDB.NewPebbleAdapter(kvStore)
	stateDB := contractDB.NewContractDB(didClient, repo)

	// 6. Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)

	// 7. Start gRPC server (blocks until ctx is cancelled)
	log.Info().Int("port", port).Msg("Starting Integrated Smart Contract gRPC server")

	if err := router.StartGRPC(ctx, port, smartRouter); err != nil {
		return fmt.Errorf("smart contract gRPC server failed: %w", err)
	}

	return nil
}
