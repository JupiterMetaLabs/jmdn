package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gossipnode/SmartContract/internal/database"
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/router"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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

	// 2. Initialize Registry (Layer 2)
	fmt.Println("   Initializing Registry...")
	registryFactory, err := contract_registry.NewRegistryFactory(dbConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry factory")
	}

	reg, err := registryFactory.CreateRegistryDB()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create registry")
	}
	defer reg.Close()

	// 3. Initialize StateDB (Layer 6)
	var stateDB *state.ImmuStateDB

	if dbConfig.Type == database.DBTypeImmuDB {
		fmt.Println("   State: Persistent (ImmuDB)")
		// reuse connection pool from registry factory logic or get new one
		// Since we want to key off the same config
		conn, err := database.GetOrCreateContractsDBPool(dbConfig)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to get DB connection for StateDB")
		}
		// Note: We don't close conn here directly as Pool manages it, but we could return it?
		// Pool logic in internal/database/pool.go says "GetOrCreate".

		stateDB = state.NewImmuStateDB(conn)
	} else {
		fmt.Println("   State: In-Memory")
		stateDB = state.NewInMemoryStateDB()
	}

	// 4. Initialize Router (Layer 3)
	fmt.Println("   Initializing Router...")
	// We need to pass the connection for the Router if it needs direct DB access later,
	// though currently it uses stateDB and contract_registry.
	// Passing the pool connection if available.
	var dbConn *config.PooledConnection
	if dbConfig.Type == database.DBTypeImmuDB {
		// This cast works because internal/database imports gossipnode/config
		// actually wait, database.PooledConnection might be type alias or struct
		// Let's check imports. database package imports "gossipnode/config"
		// and GetOrCreate returns *config.PooledConnection.
		// internal/router/router.go imports "gossipnode/SmartContract/internal/database"
		// implementation: type PooledConnection = config.PooledConnection (in database package?)
		// No, database package imports "gossipnode/config".

		// Checking internal/database/pool.go content...
		// It imports "gossipnode/config".
		// GetOrCreateContractsDBPool returns (*config.PooledConnection, error).

		var err error
		dbConn, err = database.GetOrCreateContractsDBPool(dbConfig)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to get DB connection")
		}
	}

	smartRouter := router.NewRouter(chainID, stateDB, reg, dbConn)
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
