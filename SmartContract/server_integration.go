package SmartContract

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/repository"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/SmartContract/internal/storage"
	pb "gossipnode/gETH/proto"
)

// StartIntegratedServer initializes and starts the Smart Contract gRPC server
// within the context of the main node (jmdn).
// It ensures that the database lock is shared with the existing consensus logic.
func StartIntegratedServer(ctx context.Context, port int, chainID int, gethPort int, didAddr string, blockgenPort int) error {
	log.Info().Msg("Initializing Smart Contract Service...")

	// Configure EVM Block API endpoint if provided
	if blockgenPort > 0 {
		evmEndpoint := fmt.Sprintf("http://localhost:%d", blockgenPort)
		evm.SetAPIEndpoint(evmEndpoint)
		log.Info().Str("endpoint", evmEndpoint).Msg("Configured EVM Block API endpoint")
	}

	// 1. Initialize Shared Storage (Singleton for process)
	dbConfig := database.LoadConfigFromEnv()
	storeConfig := storage.ConfigFromEnv(dbConfig)

	// Open the PebbleDB
	kvStore, err := storage.NewKVStore(storeConfig)
	if err != nil {
		return fmt.Errorf("failed to initialize KVStore for Smart Contracts: %w", err)
	}

	// CRITICAL: Share this instance with the EVM package so BlockProcessing uses it
	evm.SetSharedKVStore(kvStore)
	log.Info().Msg("Shared Access to Contract Storage initialized.")

	// 2. Initialize Registry
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		return fmt.Errorf("failed to create registry factory: %w", err)
	}

	Security.SetExpectedChainID(chainID)

	reg, err := registryFactory.CreateRegistryDB(kvStore)
	if err != nil {
		return fmt.Errorf("failed to create registry: %w", err)
	}

	// 3. Initialize ContractDB (State Layer)

	// gETH Client
	// We handle the connection lifecycle via retry logic if needed, but for now simple dial
	// Wait a bit for other servers to start? No, Dial is non-blocking.

	gethClientConn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", gethPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create gETH client connection (SmartContract)")
	}
	// TODO: Verify if we should close these connections on shutdown?
	// The context cancellation will stop the server, but connections might need explicit close.
	// For integrated server, we can likely rely on process exit, or keep them around.

	chainClient := pb.NewChainClient(gethClientConn)

	// DID Client
	didClientConn, err := grpc.NewClient(
		didAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create DID client connection (SmartContract)")
	}
	didClient := pbdid.NewDIDServiceClient(didClientConn)

	// Share the DID client with the EVM package so InitializeStateDB never
	// needs to dial a new connection or reference a hardcoded address.
	evm.SetSharedDIDClient(didClient)
	log.Info().Str("did_addr", didAddr).Msg("Shared DID client registered for EVM state initialization.")

	repo := repository.NewPebbleAdapter(kvStore)
	stateDB := state.NewContractDB(didClient, repo)

	// 4. Initialize Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)

	// 5. Start gRPC Server
	log.Info().Int("port", port).Msg("Starting Integrated Smart Contract gRPC server")

	// This blocks, so we expect the caller to run this in a goroutine
	// But StartGRPC might block? Yes.

	// We can return error if immediate failure, or block.
	// The caller (goMaybeTracked) expects a blocking function usually?
	// No, checking router.StartGRPC implementation is better.

	if err := router.StartGRPC(ctx, port, smartRouter); err != nil {
		return fmt.Errorf("smart contract gRPC server failed: %w", err)
	}

	return nil
}
