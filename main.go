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
	"gossipnode/helper"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/seed"
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

var (
	mainDBClient *config.ImmuClient // For main database operations
	didDBClient  *config.ImmuClient // For DID/accounts operations
)

func printDashes() {
	fmt.Println("\n", strings.Repeat("-", 50), "\n")
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
	// First, initialize the DID propagation system with our didDBClient
	err := messaging.InitDIDPropagation(didDBClient)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to initialize DID propagation with ImmuDB. Starting in standalone mode.")
		// We'll continue with a standalone server
	} else {
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

// Initialize ImmuDB client for main DB
func initMainDBClient() (*config.ImmuClient, error) {
	client, err := DB_OPs.New(
		DB_OPs.WithRetryLimit(3),
		DB_OPs.WithDatabase(config.DBName),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize main database client: %w", err)
	}

	log.Info().Str("database", config.DBName).Msg("Main ImmuDB client initialized")
	return client, nil
}

// Initialize ImmuDB client for accounts/DID
func initDIDDBClient() (*config.ImmuClient, error) {
	client, err := DB_OPs.NewAccountsClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize accounts database client: %w", err)
	}

	err = DB_OPs.EnableAccountsConnectionPooling(DB_OPs.DefaultConnectionPoolConfig())
	if err != nil {
		fmt.Println("Failed to initialize accounts connection pool: %v", err)
	}

	log.Info().Str("database", config.AccountsDBName).Msg("DID/Accounts ImmuDB client initialized")
	return client, nil
}

// initFastSync initializes the FastSync service
func initFastSync(n *config.Node, mainClient, accountsClient *config.ImmuClient) *fastsync.FastSync {
	fs := fastsync.NewFastSync(n.Host, mainClient, accountsClient)
	log.Info().Msg("FastSync service initialized with multi-database support")
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
	heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
	metricsPort := flag.String("metrics", "8080", "Port for Prometheus metrics")
	logDir := flag.String("logdir", "./logs", "Directory for log files")
	logToConsole := flag.Bool("console", false, "Also log to console")
	enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
	apiPort := flag.Int("api", 0, "Run ImmuDB API on specified port (0 = disabled)")
	blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
	mempoolgRPC := flag.String("mempool", "localhost:15051", "Mempool gRPC server address")
	cliGRPC := flag.Int("cli", 15053, "CLI gRPC server address")
	DIDgRPC := flag.String("did", "localhost:15052", "DID gRPC server address")
	gETHgRPC := flag.Int("geth", 15054, "gETH gRPC server address")

	flag.Parse()

	// Initialize logger
	logFileName := fmt.Sprintf("p2p-node.log")
	if err := logging.InitLogger(*logDir, logFileName, *logToConsole); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}

	// Clean up any leftover temp files from a previous run
	defer logging.Close()

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
		os.Exit(0)
	}()

	// Start the node
	n, err := node.NewNode()
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()

	// Set the stream handler for receiving files for fastsync. This is crucial
	// for the final phase of the sync process.
	n.Host.SetStreamHandler(config.FileProtocol, func(s network.Stream) {
		// Use an empty string for outputPath to use the default path in HandleFileStream
		transfer.HandleFileStream(s, "")
	})


	// Initialize main database client
	immuClient, err = initMainDBClient()
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize main database client")
		os.Exit(1)
		// Handle error or continue with degraded functionality
	}
	defer DB_OPs.Close(immuClient)

	// Initialize Connection Pool
	DB_OPs.InitGlobalPool(&DB_OPs.PoolingConfig{
		DBAddress:  config.DBAddress,
		DBPort:     config.DBPort,
		DBName:     config.DBName,
		DBUsername: config.DBUsername,
		DBPassword: config.DBPassword,
	})

	// Later when you need to use the pool:
	DefaultDB_Pool := DB_OPs.GetGlobalPool()

	// Initialize DID database client - can be done later if needed
	didDBClient, err = initDIDDBClient()
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize DID database client")
		os.Exit(1) // Exit if DID client is critical
	}
	defer DB_OPs.Close(didDBClient)

	// Initilize Connection Pool for accounts database
	DB_OPs.InitGlobalPool(&DB_OPs.PoolingConfig{
		DBAddress:  config.DBAddress,
		DBPort:     config.DBPort,
		DBName:     config.AccountsDBName,
		DBUsername: config.DBUsername,
		DBPassword: config.DBPassword,
	})

	// Later when you need to use the pool:
	AccountsDB_Pool := DB_OPs.GetGlobalPool()

	// Initialize FastSync service
	fastSyncer = initFastSync(n, immuClient, didDBClient)

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
	nodeManager, err = node.NewNodeManager(n)
	if err != nil {
		fmt.Printf("Failed to initialize node manager: %v\n", err)
		return
	}
	nodeManager.StartHeartbeat(*heartbeatInterval)
	defer nodeManager.Shutdown()

	// Initialize DID propagation handler
	n.Host.SetStreamHandler(config.DIDPropagationProtocol, messaging.HandleDIDStream)

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
			Block.Startserver(*blockgen, n.Host)
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
	
	if *gETHgRPC > 0 {
		go func() {
			fmt.Printf("Starting gETH gRPC server on port %d\n", *gETHgRPC)
			if err := gETH.StartGRPC(*gETHgRPC); err != nil {
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
		SeedNode:        *connect,
		EnableYggdrasil: *enableYggdrasil,
	}

	// Only set database clients if they're properly initialized
	if immuClient != nil {
		cmdHandler.MainClient = immuClient
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

	if err := cmdHandler.StartCLI(*cliGRPC); err != nil {
		log.Error().Err(err).Msg("Failed to start CLI")
	}

}
