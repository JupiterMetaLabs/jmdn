package SmartContract

import (
	"context"
	"fmt"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gossipnode/DB_OPs"
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

	// Build the loopback EVM block context in-process (header-only reads)
	// instead of HTTP GETs of the full tip block (proof included) per eth_call.
	// The HTTP endpoint above stays as the fallback when no reader is set.
	evm.SetChainReader(&evm.ChainReader{Head: evmHead, HashAt: evmHashAt})

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

// evmHead reports the same tip /api/latest-block serves — the data-complete tip
// (D-858) — from the blocks row only. Tip 0 is "no blocks yet": an error makes
// the EVM keep its defaults (30M gas, time.Now()) rather than the HTTP path's
// zero-filled context.
func evmHead() (evm.HeadInfo, error) {
	tip, err := DB_OPs.GetLatestDataCompleteBlock()
	if err != nil {
		return evm.HeadInfo{}, err
	}
	if tip == 0 {
		return evm.HeadInfo{}, fmt.Errorf("no blocks in the chain yet")
	}
	blk, err := DB_OPs.GetZKBlockHeaderByNumber(tip)
	if err != nil {
		return evm.HeadInfo{}, err
	}
	ts := uint64(0)
	if blk.Timestamp > 0 {
		ts = uint64(blk.Timestamp)
	}
	h := evm.HeadInfo{Number: blk.BlockNumber, Timestamp: ts, GasLimit: blk.GasLimit}
	if blk.CoinbaseAddr != nil {
		h.Coinbase = *blk.CoinbaseAddr
	}
	return h, nil
}

// evmHashAt returns the stored hash of block n (BLOCKHASH opcode, loopback only).
func evmHashAt(n uint64) (common.Hash, error) {
	blk, err := DB_OPs.GetZKBlockHeaderByNumber(n)
	if err != nil {
		return common.Hash{}, err
	}
	return blk.BlockHash, nil
}
