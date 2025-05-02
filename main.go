package main

import (
	// "bufio"
	"context"
	"flag"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	// "sync"
	"syscall"
	"time"

	"gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/DID"
	"gossipnode/config"
	"gossipnode/explorer"
	fastsync "gossipnode/fastsync"
	"gossipnode/helper"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
    cli "gossipnode/CLI"
	"gossipnode/seed"

	"github.com/libp2p/go-libp2p/core/host"
	// "github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/mattn/go-sqlite3"
	// ma "github.com/multiformats/go-multiaddr"
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

// StartExplorerServer starts the explorer server on the given address
func StartExplorerServer(address string) error {
    explorer, err := explorer.NewExplorer()
    if err != nil {
        return err
    }
    
    helper.SetBroadcastHandler(explorer)

    r := explorer.SetupRoutes()

    log.Info().Str("address", address).Msg("Starting ImmuDB Explorer server")
    return http.ListenAndServe(address, r)
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

    // Command-line flags for node configuration
    isSeed := flag.Bool("seed", false, "Run as a seed node")
    connect := flag.String("connect", "", "Connect to a seed node (multiaddr)")
    heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
    metricsPort := flag.String("metrics", "8080", "Port for Prometheus metrics")
    logDir := flag.String("logdir", "./logs", "Directory for log files")
    logToConsole := flag.Bool("console", false, "Also log to console")
    enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
    explorerPort := flag.Int("explorer", 0, "Run blockchain explorer on specified port (0 = disabled)")
    apiPort := flag.Int("api", 0, "Run ImmuDB API on specified port (0 = disabled)")
    blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
    mempoolgRPC := flag.String("mempool", "localhost:15051", "Mempool gRPC server address")
    DIDgRPC := flag.String("did", "localhost:15052", "DID gRPC server address")

    flag.Parse()

    // Initialize logger
    logFileName := fmt.Sprintf("p2p-node.log")
    if err := logging.InitLogger(*logDir, logFileName, *logToConsole); err != nil {
        fmt.Printf("Failed to initialize logger: %v\n", err)
        return
    }
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

    // Initialize main database client
    immuClient, err = initMainDBClient()
    if err != nil {
        log.Error().Err(err).Msg("Failed to initialize main database client")
        // Handle error or continue with degraded functionality
    }
    defer DB_OPs.Close(immuClient)

    // Initialize DID database client - can be done later if needed
    didDBClient, err = initDIDDBClient()
    if err != nil {
        log.Error().Err(err).Msg("Failed to initialize DID database client")
        // Handle error or continue with degraded functionality
    }
    defer DB_OPs.Close(didDBClient)

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
    fmt.Printf(config.ColorGreen+"\nMetrics available at "+ config.ColorReset+ "http://localhost%s/metrics\n", metricsAddr)

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

    if *explorerPort > 0 {
        mime.AddExtensionType(".css", "text/css")
        mime.AddExtensionType(".js", "application/javascript")
        mime.AddExtensionType(".html", "text/html")
        mime.AddExtensionType(".svg", "image/svg+xml")
        mime.AddExtensionType(".json", "application/json")
        go func() {
            log.Info().Msgf("Starting blockchain explorer on port %d", *explorerPort)
            fmt.Printf("\nBlockchain explorer available at http://localhost:%d\n", *explorerPort)
            
            // Initialize explorer
            explorerAddr := fmt.Sprintf(":%d", *explorerPort)
            if err := StartExplorerServer(explorerAddr); err != nil {
                log.Error().Err(err).Msg("Failed to start explorer server")
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
        fmt.Println(config.ColorGreen+"Main database client connected"+config.ColorReset)
    } else {
        fmt.Println(config.ColorYellow+"Warning: Main database client not available - some commands disabled"+config.ColorReset)
    }
    
    if didDBClient != nil {
        cmdHandler.DIDClient = didDBClient
        fmt.Println(config.ColorGreen+"DID database client connected"+config.ColorReset)
    } else {
        fmt.Println(config.ColorYellow+"Warning: DID database client not available - some commands disabled"+config.ColorReset)
    }
    
    if err := cmdHandler.StartCLI(); err != nil {
        log.Error().Err(err).Msg("Failed to start CLI")
    }
    
}