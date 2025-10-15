package main

import (
	// "bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"syscall"
	"time"

	"gossipnode/Block"
	"gossipnode/CA/ImmuDB_CA"
	cli "gossipnode/CLI"
	"gossipnode/DB_OPs"
	"gossipnode/DID"
	"gossipnode/config"
	"gossipnode/explorer"
	fastsync "gossipnode/fastsync"
	"gossipnode/gETH"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/rpc"
	"gossipnode/helper"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/seed"
	"gossipnode/seednode"
	"gossipnode/transfer"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"
)

// Simple helper to print the CLI prompt in color
func printPrompt() {
	fmt.Printf(config.ColorGreen + ">>> " + config.ColorReset)
}

// Global variables for easier access
var (
	fastSyncer *fastsync.FastSync
	immuClient *config.ImmuClient
)

// Global connection pools
var (
	mainDBPool     *config.ConnectionPool // Main database connection pool
	accountsDBPool *config.ConnectionPool // Accounts/DID database connection pool
)

func StartFacadeServer(port int, chainID int) {
	go func() {
		log.Info().Msg("Starting facade server")
		// Get the Http Server
		HTTPServer := rpc.NewHandlers(Service.NewService(chainID))
		httpServer := rpc.NewHTTPServer(HTTPServer)
		if err := httpServer.Serve(fmt.Sprintf("0.0.0.0:%d", port)); err != nil {
			log.Error().Err(err).Msg("Failed to start facade server")
		}
	}()
}

func StartWSServer(port int, chainID int) {
	go func() {
		log.Info().Msg("Starting WSServer")
		// Get the Http Server
		HTTPServer := rpc.NewHandlers(Service.NewService(chainID))

		WSServer := rpc.NewWSServer(HTTPServer, Service.NewService(chainID))
		if err := WSServer.Serve(fmt.Sprintf("0.0.0.0:%d", port)); err != nil {
			log.Error().Err(err).Msg("Failed to start WSServer")
		}
	}()
}

// GetMainDBPool returns the global main database connection pool
func GetMainDBPool() *config.ConnectionPool {
	if mainDBPool == nil {
		log.Fatal().Msg("Main DB pool not initialized. Call initMainDBPool first")
	}
	return mainDBPool
}

// GetAccountsDBPool returns the global accounts database connection pool
func GetAccountsDBPool() *config.ConnectionPool {
	if accountsDBPool == nil {
		log.Fatal().Msg("Accounts DB pool not initialized. Call initAccountsDBPool first")
	}
	return accountsDBPool
}

func printDashes() {
	fmt.Println("\n" + strings.Repeat("-", 50) + "\n")
}

func StartAPIServer(address string) error {
	// Create ImmuDB API server
	server, err := explorer.NewImmuDBServer()
	if err != nil {
		return fmt.Errorf("failed to create ImmuDB API server: %w", err)
	}

	go explorer.StartBlockPoller(server, 7*time.Second)

	log.Info().Str("address", address).Msg("Starting ImmuDB API server")
	return server.Start(address)
}

// Update this function:
func startDIDServer(h host.Host, address string) error {
	didDBClient, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		//Debugging
		fmt.Println("Failed to get DID database client", err)

		log.Warn().Err(err).Msg("Failed to initialize DID propagation with ImmuDB. Starting in standalone mode.")
		// We'll continue with a standalone server
	} else {
		//Debugging
		fmt.Println("Got DID database client successfully", didDBClient)

		log.Info().Msg("DID propagation initialized successfully")
	}
	// Start the DID server with our existing client
	return DID.StartDIDServer(h, address, didDBClient)
}

// initYggdrasilMessaging initializes the Yggdrasil messaging system
func initYggdrasilMessaging(ctx context.Context) {
	directMSG.StartYggdrasilListener(ctx)
	fmt.Println(config.ColorGreen+"Yggdrasil messaging service started on port:"+config.ColorReset, directMSG.YggdrasilPort)
}

// Initialize main database connection pool
func initMainDBPool(enableLoki bool) error {
	poolingConfig := &config.PoolingConfig{
		DBAddress:  config.DBAddress,
		DBPort:     config.DBPort,
		DBName:     config.DBName,
		DBUsername: config.DBUsername,
		DBPassword: config.DBPassword,
	}

	// Initialize the global pool
	config.InitGlobalPoolWithLoki(poolingConfig, enableLoki)
	mainDBPool = config.GetGlobalPool()

	// Also initialize the DB_OPs main pool
	fmt.Println("Initializing DB_OPs main pool...")
	poolConfig := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPoolWithLoki(poolConfig, enableLoki); err != nil {
		return fmt.Errorf("failed to initialize DB_OPs main pool: %w", err)
	}
	fmt.Println("DB_OPs main pool initialized successfully")

	log.Info().Str("database", config.DBName).Msg("Main database connection pool initialized")
	return nil
}

// Initialize accounts database connection pool
func initAccountsDBPool(enableLoki bool) error {
	// Use the DB_OPs package to initialize the accounts pool
	// This ensures the database exists and the pool is properly configured
	if err := DB_OPs.InitAccountsPoolWithLoki(enableLoki); err != nil {
		return fmt.Errorf("failed to initialize accounts database pool: %w", err)
	}

	log.Info().Str("database", config.AccountsDBName).Msg("Accounts database connection pool initialized")
	return nil
}

// initFastSync initializes the FastSync service
func initFastSync(n *config.Node, mainClient *config.PooledConnection, accountsClient *config.PooledConnection) *fastsync.FastSync {

	fs := fastsync.NewFastSync(n.Host, mainClient, accountsClient)
	log.Info().Msg("FastSync service initialized - will get connections when needed")
	return fs
}

func main() {
	var nodeManager *node.NodeManager
	if err := ImmuDB_CA.EnsureTLSAssets(".immudb_state"); err != nil {
		fmt.Printf("Failed to ensure TLS assets: %v\n", err)
		log.Fatal()
	}
	fmt.Println("ImmuDB TLS assets generated.")

	// Command-line flags for node configuration
	isSeed := flag.Bool("seed", false, "Run as a seed node")
	connect := flag.String("connect", "", "Connect to a seed node (multiaddr)")
	seedNodeURL := flag.String("seednode", "", "Seed node gRPC URL for peer registration (e.g., localhost:9090)")
	peerAlias := flag.String("alias", "", "Peer alias for registration with seed node")
	enableLoki := flag.Bool("loki", false, "Enable Loki logging (default: false)")
	heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
	metricsPort := flag.String("metrics", "8080", "Port for Prometheus metrics")
	enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
	apiPort := flag.Int("api", 0, "Run ImmuDB API on specified port (0 = disabled)")
	blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
	mempoolgRPC := flag.String("mempool", "localhost:15051", "Mempool gRPC server address")
	cliGRPC := flag.Int("cli", 15053, "CLI gRPC server address")
	DIDgRPC := flag.String("did", "localhost:15052", "DID gRPC server address")
	gETHgRPC := flag.Int("geth", 15054, "gETH gRPC server address")
	gETHFacade := flag.Int("facade", 15001, "gETH Facade server address")
	gETHWSServer := flag.Int("ws", 15002, "gETH WSServer address")
	chainID := flag.Int("chainID", 7000700, "Chain ID for the blockchain network")
	flag.Parse()

	// Initialize logger
	logFileName := "p2p-node.log"
	Logger, err := logging.ReturnDefaultLoggerWithLoki(logFileName, "p2p-node", *enableLoki)
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}
	// Clean up any leftover temp files from a previous run
	defer Logger.Close()

	// Create a cancellable context for clean shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutdown signal received, closing connections...")
		cancel() // Cancel the context
		// Give some time for cleanup
		time.Sleep(500 * time.Millisecond)
		os.Exit(1)
	}()

	// Initialize database connection pools FIRST
	fmt.Println("Initializing main database pool...")
	if err := initMainDBPool(*enableLoki); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize main database pool")
	}
	fmt.Println("Main database pool initialized successfully")

	if err := initAccountsDBPool(*enableLoki); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize accounts database pool")
	}

	// Start the node
	fmt.Println("Creating libp2p node...")
	n, err := node.NewNode()
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()
	fmt.Println("Node created successfully")

	// Set the stream handler for receiving files for fastsync. This is crucial
	// for the final phase of the sync process.
	n.Host.SetStreamHandler(config.FileProtocol, func(s network.Stream) {
		// Use an empty string for outputPath to use the default path in HandleFileStream
		transfer.HandleFileStream(s, "")
	})

	// Initialize database clients using the pools
	mainDBClient, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get main database connection from pool")
	}
	defer func() {
		if mainDBClient != nil {
			DB_OPs.PutMainDBConnection(mainDBClient)
		}
	}()

	// Debugging
	fmt.Println("Getting accounts database connection from pool")

	didDBClient, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get accounts database connection from pool")
	}

	// Debugging
	fmt.Println("Got accounts database connection from pool", didDBClient)

	defer func() {
		if didDBClient != nil {
			DB_OPs.PutAccountsConnection(didDBClient)
		}
	}()

	// Initialize FastSync service
	fastSyncer = initFastSync(n, mainDBClient, didDBClient)

	// Initialize Yggdrasil messaging if enabled
	if *enableYggdrasil {
		initYggdrasilMessaging(ctx)
		log.Info().Msgf("Yggdrasil messaging enabled on port %d", directMSG.YggdrasilPort)
	}

	// Display node identity
	ipv6, err := helper.GetTun0GlobalIPv6()
	if err != nil || ipv6 == "" {
		ipv6 = "?"
		log.Printf("Error getting tun0 IPv6 address: %v", err)
	}
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Address:"+config.ColorReset, ipv6)
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Full Peer Address:"+config.ColorReset, "/ip6/"+ipv6+"/tcp/15000/p2p/"+n.Host.ID().String())

	fmt.Println(config.ColorGreen+"Node ID:"+config.ColorReset, n.Host.ID().String())
	fmt.Println("Addresses:")
	for _, addr := range n.Host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
	}

	if mempoolgRPC == nil {
		log.Printf("No mempool gRPC address provided; cannot proceed.")
	}

	address := *mempoolgRPC
	if err := Block.InitMempoolClient(address); err != nil {
		log.Printf("Failed to connect to mempool: %v", err)
	}
	defer Block.CloseMempoolClient()

	// Start metrics server (just once)
	metricsAddr := ":" + *metricsPort
	metrics.StartMetricsServer(metricsAddr)
	fmt.Printf(config.ColorGreen+"\nMetrics available at "+config.ColorReset+"http://localhost%s/metrics\n", metricsAddr)

	// Initialize node manager
	nodeManager, err = node.NewNodeManagerWithLoki(n, *enableLoki)
	if err != nil {
		fmt.Printf("Failed to initialize node manager: %v\n", err)
		return
	}
	// Debugging
	fmt.Println("Node manager initialized successfully", nodeManager)

	nodeManager.StartHeartbeat(*heartbeatInterval)
	defer nodeManager.Shutdown()

	// Initialize DID propagation handler
	n.Host.SetStreamHandler(config.DIDPropagationProtocol, messaging.HandleDIDStream)

	// Initialize DID propagation system
	if err := messaging.InitDIDPropagation(nil); err != nil {
		fmt.Printf("Failed to initialize DID propagation: %v\n", err)
		log.Error().Err(err).Msg("Failed to initialize DID propagation")
	}

	// We'll initialize the DID system in the DID server to avoid blocking main
	go func() {
		log.Info().Str("address", *DIDgRPC).Msg("Starting DID gRPC server")
		if err := startDIDServer(n.Host, *DIDgRPC); err != nil {
			fmt.Println("Failed to start DID gRPC server:", err)
			log.Error().Err(err).Msg("Failed to start DID gRPC server")
		}
	}()

	if *blockgen > 0 {
		go func() {
			log.Info().Msgf("Starting block generator on port %d", *blockgen)
			fmt.Printf("\nBlock generator available at http://localhost:%d\n", *blockgen)
			Block.Startserver(*blockgen, n.Host, *chainID)
		}()
	}
	// Configure as seed node if requested
	if *isSeed {
		err = seed.RegisterAsSeed(n)
		if err != nil {
			fmt.Printf("Failed to register as seed node: %v\n", err)
			return
		}
		fmt.Println("Running as a seed node")
	}

	// Connect to a seed node if requested
	if *connect != "" {
		fmt.Printf("Connecting to seed node: %s\n", *connect)
		peers, err := seed.RequestPeers(n.Host, *connect, 10, "")
		if err != nil {
			fmt.Printf("Error connecting to seed: %v\n", err)
		} else {
			fmt.Printf("Connected to seed. Discovered %d peers\n", len(peers))
			for i, p := range peers {
				fmt.Printf("  %d. ID: %s, Addresses: %v\n", i+1, p.ID, p.Addrs)
			}
		}
	}

	// Register with seed node gRPC if URL is provided
	if *seedNodeURL != "" {
		fmt.Printf("Registering with seed node gRPC: %s\n", *seedNodeURL)
		seedClient, err := seednode.NewClient(*seedNodeURL)
		if err != nil {
			fmt.Printf("Failed to create seed node client: %v\n", err)
			log.Error().Err(err).Msg("Failed to create seed node client")
		} else {
			defer seedClient.Close()

			// Register this peer with the seed node (with or without alias)
			if *peerAlias != "" {
				fmt.Printf("Registering with alias: %s\n", *peerAlias)
				err = seedClient.RegisterPeerWithAlias(n.Host, *peerAlias)
				if err != nil {
					fmt.Printf("Failed to register with seed node using alias: %v\n", err)
					log.Error().Err(err).Msg("Failed to register with seed node using alias")
				} else {
					fmt.Printf("Successfully registered with seed node using alias '%s'\n", *peerAlias)
					log.Info().Str("alias", *peerAlias).Msg("Successfully registered with seed node using alias")
				}
			} else {
				err = seedClient.RegisterPeer(n.Host)
				if err != nil {
					fmt.Printf("Failed to register with seed node: %v\n", err)
					log.Error().Err(err).Msg("Failed to register with seed node")
				} else {
					fmt.Println("Successfully registered with seed node")
					log.Info().Msg("Successfully registered with seed node")
				}
			}
		}
	}

	if *gETHgRPC > 0 {
		go func() {
			fmt.Printf("Starting gETH gRPC server on port %d\n", *gETHgRPC)
			if err := gETH.StartGRPC(*gETHgRPC, *chainID); err != nil {
				log.Error().Err(err).Msg("gETH gRPC server error")
			}
		}()
	}

	if *apiPort > 0 {
		go func() {
			log.Info().Msgf("Starting ImmuDB API on port %d", *apiPort)
			fmt.Printf("\nImmuDB API available at http://localhost:%d/api\n", *apiPort)

			// Initialize API server
			apiAddr := fmt.Sprintf(":%d", *apiPort)
			if err := StartAPIServer(apiAddr); err != nil {
				log.Error().Err(err).Msg("Failed to start API server")
			}
		}()
	}

	cmdHandler := &cli.CommandHandler{
		Node:            n,
		NodeManager:     nodeManager,
		FastSyncer:      fastSyncer,
		SeedNode:        *seedNodeURL,
		EnableYggdrasil: *enableYggdrasil,
	}

	// Only set database clients if they're properly initialized
	if mainDBClient != nil {
		cmdHandler.MainClient = mainDBClient
		fmt.Println(config.ColorGreen + "Main database client connected" + config.ColorReset)
	} else {
		fmt.Println(config.ColorYellow + "Warning: Main database client not available - some commands disabled" + config.ColorReset)
	}

	if didDBClient != nil {
		cmdHandler.DIDClient = didDBClient
		fmt.Println(config.ColorGreen + "DID database client connected" + config.ColorReset)
	} else {
		fmt.Println(config.ColorYellow + "Warning: DID database client not available - some commands disabled" + config.ColorReset)
	}

	if *gETHFacade > 0 {
		fmt.Printf("Starting gETH Facade server on port %d\n", *gETHFacade)
		StartFacadeServer(*gETHFacade, *chainID)
	}

	if *gETHWSServer > 0 {
		fmt.Printf("Starting gETH WSServer on port %d\n", *gETHWSServer)
		StartWSServer(*gETHWSServer, *chainID)
	}

	// Start CLI without timeout - run indefinitely
	done := make(chan error, 1)
	go func() {
		done <- cmdHandler.StartCLI(*cliGRPC)
	}()

	// Wait for CLI to complete or error
	if err := <-done; err != nil {
		log.Error().Err(err).Msg("Failed to start CLI")
	}
}
