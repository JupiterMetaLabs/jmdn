package SmartContract

import (
	"context"
	"fmt"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gossipnode/DB_OPs/cassata"
	contractDB "gossipnode/DB_OPs/contractDB"
	pbdid "gossipnode/DID/proto"
	"gossipnode/Security"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/router"
	pb "gossipnode/gETH/proto"
)

// StartIntegratedServer initialises and starts the Smart Contract gRPC server
// within the context of the main JMDN node, sharing the process-wide DB lock.
// cas must be non-nil — it provides both the KV store (for hot-path EVM state)
// and the SQL projection (for receipts and the contract registry).
func StartIntegratedServer(ctx context.Context, port int, chainID int, gethPort int, didAddr string, blockgenPort int, cas *cassata.Cassata) error {
	logger().Info(ctx, "Initializing Smart Contract Service...")

	if cas == nil {
		return fmt.Errorf("cassata is nil — ThebeDB is required")
	}

	if blockgenPort > 0 {
		evmEndpoint := fmt.Sprintf("http://localhost:%d", blockgenPort)
		evm.SetAPIEndpoint(evmEndpoint)
		logger().Info(ctx, "Configured EVM Block API endpoint",
			ion.String("endpoint", evmEndpoint))
	}

	Security.SetExpectedChainID(chainID)

	// 1. Contract Registry — ThebeDB-backed (persists across restarts).
	reg := contract_registry.NewThebeRegistryDB(cas)
	logger().Info(ctx, "ContractRegistry: using ThebeRegistryDB (persisted)")

	// 2. gETH gRPC client
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

	// 3. DID gRPC client
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

	// 4. ContractDB (State Layer) — KV-backed hot path for code/storage/nonce/meta;
	//    SQL-backed for receipts (via cassata).
	repo := contractDB.NewKVStateRepository(cas.KV(), cas)
	contractDB.SetSharedStateRepository(repo)
	logger().Info(ctx, "ContractDB: using KVStateRepository (BadgerDB hot path)")
	stateDB := contractDB.NewContractDB(didClient, repo)

	// 5. Router
	smartRouter := router.NewRouter(chainID, stateDB, reg, nil, chainClient)

	// 6. Start gRPC server (blocks until ctx is cancelled)
	logger().Info(ctx, "Starting Integrated Smart Contract gRPC server",
		ion.Int("port", port))

	if err := router.StartGRPC(ctx, port, smartRouter); err != nil {
		return fmt.Errorf("smart contract gRPC server failed: %w", err)
	}

	return nil
}
