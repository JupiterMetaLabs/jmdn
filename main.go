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

	"gossipnode/config/GRO"

	orchestratorGlobal "github.com/JupiterMetaLabs/goroutine-orchestrator/manager/global"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Block"
	"gossipnode/CA/ImmuDB_CA"
	cli "gossipnode/CLI"
	"gossipnode/DB_OPs"
	"gossipnode/DID"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"gossipnode/config/version"
	"gossipnode/explorer"
	fastsync "gossipnode/fastsync"
	"gossipnode/gETH"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/rpc"
	"gossipnode/helper"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/seednode"
	"gossipnode/transfer"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"

	"gossipnode/SmartContract"
)

var (
	MainAM interfaces.AppGoroutineManagerInterface
	MainLM interfaces.LocalGoroutineManagerInterface
)

var groTrackingEnabled bool

func shouldEnableGROTracking(grotrack bool, metricsPort string) bool {
	if !grotrack {
		return false
	}
	if metricsPort == "" || metricsPort == "0" {
		return false
	}
	return true
}

func goMaybeTracked(
	localMgr interfaces.LocalGoroutineManagerInterface,
	appName string,
	localName string,
	threadName string,
	fn func(ctx context.Context) error,
	opts ...interfaces.GoroutineOption,
) error {
	if localMgr == nil {
		return fmt.Errorf("local manager is nil (thread=%s)", threadName)
	}
	if groTrackingEnabled {
		return metrics.GoTracked(localMgr, appName, localName, threadName, fn, opts...)
	}
	return localMgr.Go(threadName, fn, opts...)
}

// Simple helper to print the CLI prompt in color
func printPrompt() {
	fmt.Printf(config.ColorGreen + ">>> " + config.ColorReset)
}

// Global variables for easier access
var (
	fastSyncer   *fastsync.FastSync
	immuClient   *config.ImmuClient
	globalPubSub *Pubsub.StructGossipPubSub
)

// Global connection pools
var (
	mainDBPool     *config.ConnectionPool // Main database connection pool
	accountsDBPool *config.ConnectionPool // Accounts/DID database connection pool
)

func initGlobalGRO() {
	// This is the creation an setting of the global GRO manager
	GRO.InitGlobal()

	// Ensure global manager is initialized before we mutate metadata.
	if _, err := GRO.GlobalGRO.Init(); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize global GRO manager")
	}

	// Set the global shutdown timeout to 10 seconds.
	if _, err := GRO.GlobalGRO.UpdateMetadata(
		orchestratorGlobal.SET_SHUTDOWN_TIMEOUT,
		10*time.Second,
	); err != nil {
		log.Fatal().Err(err).Msg("Failed to set GRO shutdown timeout metadata")
	}
}

func initAppandLocalGRO() {

	var err error
	// Also pull up new app manager - main for the main package
	err = GRO.EagerLoading()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to eager load GRO")
	}

	MainAM = GRO.GetApp(GRO.MainAM)

	MainLM, err = MainAM.NewLocalManager(GRO.MainLM)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create local manager")
	}
}

func StartFacadeServer(port int, chainID int, smartRPC int) {
	if MainLM == nil {
		log.Fatal().Msg("MainLM not initialized. Call initAppandLocalGRO() first")
	}

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.FacadeThread, func(ctx context.Context) error {
		log.Info().Msg("Starting facade server")

		handler := rpc.NewHandlers(Service.NewService(chainID, smartRPC))
		httpServer := rpc.NewHTTPServer(handler)

		addr := fmt.Sprintf("0.0.0.0:%d", port)
		if err := httpServer.ServeWithContext(ctx, addr); err != nil {
			log.Error().Err(err).Str("addr", addr).Msg("Facade server stopped")
			return fmt.Errorf("facade server failed: %w", err)
		}
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.FacadeThread).Msg("Failed to start GRO goroutine")
	}
}

func StartWSServer(port int, chainID int, smartRPC int) {
	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.WSServerThread, func(ctx context.Context) error {
		log.Info().Msg("Starting WSServer")
		// Get the Http Server
		HTTPServer := rpc.NewHandlers(Service.NewService(chainID, smartRPC))

		WSServer := rpc.NewWSServer(HTTPServer, Service.NewService(chainID, smartRPC))
		if err := WSServer.ServeWithContext(ctx, fmt.Sprintf("0.0.0.0:%d", port)); err != nil {
			log.Error().Err(err).Msg("Failed to start WSServer")
			return fmt.Errorf("WSServer failed: %w", err)
		}
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.WSServerThread).Msg("Failed to start GRO goroutine")
	}
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

// GetGlobalPubSub returns the global PubSub instance
func GetGlobalPubSub() *Pubsub.StructGossipPubSub {
	if globalPubSub == nil {
		log.Warn().Msg("Global PubSub not initialized - PubSub features may be limited")
	}
	return globalPubSub
}

func printDashes() {
	fmt.Println("\n" + strings.Repeat("-", 50) + "\n")
}

// formatTimestamp formats a time.Time as "DD-MM-YYYY HH:MM:SS" (readable format)
// Converts UTC time to local time before formatting
func formatTimestamp(t time.Time) string {
	// Convert UTC to local time
	localTime := t.Local()
	return localTime.Format("02-01-2006 15:04:05")
}

// runCommand executes a CLI command via gRPC to the running service
func runCommand(command string, args []string, grpcPort int) {
	// Special handling for version command - we want it to work even if node is offline
	if command == "version" {
		fmt.Println("Local Binary Version:")
		fmt.Println(version.String())
		fmt.Println("----------------------------------------")

		client, err := cli.NewClient(fmt.Sprintf("localhost:%d", grpcPort))
		if err == nil {
			defer client.Close()
			v, err := client.GetNodeVersion()
			if err == nil {
				fmt.Println("Remote Node Version (Running):")
				fmt.Printf("Tag: %s, Branch: %s, Commit: %s, Built: %s, Go: %s\n",
					v.GitTag, v.GitBranch, v.GitCommit, v.BuildTime, v.GoVersion)
			} else {
				// Connected but call failed
				fmt.Printf("Could not fetch remote version: %v\n", err)
			}
		} else {
			// Could not connect
			fmt.Println("Could not connect to running node (Offline?).")
		}
		os.Exit(0)
	}

	client, err := cli.NewClient(fmt.Sprintf("localhost:%d", grpcPort))
	if err != nil {
		fmt.Printf("Error connecting to gRPC server: %v\n", err)
		fmt.Println("Make sure the service is running.")
		os.Exit(1)
	}
	defer client.Close()

	switch command {

	case "help":
		fmt.Println("\nAvailable CLI Commands:")
		fmt.Println("  listpeers, list     - List all managed peers")
		fmt.Println("  addrs                - Show node addresses")
		fmt.Println("  stats                - Show messaging statistics")
		fmt.Println("  dbstate              - Show database state")
		fmt.Println("  addpeer <addr>       - Add a peer")
		fmt.Println("  removepeer <id>      - Remove a peer")
		fmt.Println("  cleanpeers           - Clean offline peers")
		fmt.Println("  sendmsg <tgt> <msg>  - Send message")
		fmt.Println("  broadcast <msg>      - Broadcast message")
		fmt.Println("  getdid <did>         - Get DID document")
		fmt.Println("  propagatedid <did> <public_key> [balance] - Propagate DID to network")
		fmt.Println("  fastsync <peer>      - Fast sync with peer")
		fmt.Println("  firstsync <peer> <server|client> - First sync: get all data from peer (server) or receive all data (client)")
		fmt.Println("\nUsage: ./jmdn -cmd <command> [args...]")
		fmt.Println("\nNote: Some interactive commands (mempoolStats, seednodeStats, etc.)")
		fmt.Println("are only available in interactive mode.")

	case "listpeers", "list":
		peers, err := client.ListPeers()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nPeers (%d):\n", len(peers.Peers))
		for _, peer := range peers.Peers {
			status := "OFFLINE"
			if peer.IsAlive {
				status = "ONLINE"
			}
			fmt.Printf("  %s - %s [%s] Last: %s\n",
				peer.Id, peer.Multiaddr, status, peer.LastSeen)
		}

	case "addrs":
		addrs, err := client.ReturnAddrs()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nNode Addresses:\n")
		for _, addr := range addrs.Peers {
			fmt.Printf("  %s\n", addr)
		}

	case "stats":
		stats, err := client.GetMessageStats()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nMessaging Statistics:\n")
		fmt.Printf("  Sent:     %d\n", stats.MessagesSent)
		fmt.Printf("  Received: %d\n", stats.MessagesReceived)
		fmt.Printf("  Failed:   %d\n", stats.MessagesFailed)

	case "dbstate":
		state, err := client.GetDatabaseState()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nDatabase State:\n")
		fmt.Printf("  Main DB TxID:     %d\n", state.MainDb.TxId)
		fmt.Printf("  Accounts DB TxID: %d\n", state.AccountsDb.TxId)

	case "addpeer":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd addpeer <peer_multiaddr>")
			os.Exit(1)
		}
		resp, err := client.AddPeer(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "removepeer":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd removepeer <peer_id>")
			os.Exit(1)
		}
		resp, err := client.RemovePeer(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "cleanpeers":
		resp, err := client.CleanPeers()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Cleaned %d peers\n", resp.CleanedCount)

	case "sendmsg":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd sendmsg <target> <message>")
			os.Exit(1)
		}
		resp, err := client.SendMessage(args[0], strings.Join(args[1:], " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "broadcast":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd broadcast <message>")
			os.Exit(1)
		}
		resp, err := client.BroadcastMessage(strings.Join(args, " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "getdid":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd getdid <did>")
			os.Exit(1)
		}
		doc, err := client.GetDID(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nDID Document:\n")
		fmt.Printf("  DID:       %s\n", doc.Did)
		fmt.Printf("  PublicKey: %s\n", doc.PublicKey)
		fmt.Printf("  Balance:   %s\n", doc.Balance)

		// Format CreatedAt timestamp as DD-MM-YYYY HH:MM:SS
		if doc.CreatedAt != nil {
			createdAt := doc.CreatedAt.AsTime()
			fmt.Printf("  CreatedAt: %s\n", formatTimestamp(createdAt))
		}

		// Format UpdatedAt timestamp as DD-MM-YYYY HH:MM:SS
		if doc.UpdatedAt != nil {
			updatedAt := doc.UpdatedAt.AsTime()
			fmt.Printf("  UpdatedAt: %s\n", formatTimestamp(updatedAt))
		}

	case "propagatedid":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd propagatedid <did> <public_key> [balance]")
			os.Exit(1)
		}
		balance := "0"
		if len(args) >= 3 {
			balance = args[2]
		}
		resp, err := client.PropagateDID(args[0], args[1], balance)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if resp.Success {
			fmt.Printf("Success: %s\n", resp.Message)
		} else {
			fmt.Printf("Error: %s\n", resp.Message)
			os.Exit(1)
		}

	case "fastsync":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd fastsync <peer_multiaddr>")
			os.Exit(1)
		}
		fmt.Println("Starting fast sync...")
		stats, err := client.FastSync(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		// Defensive guards against nil responses to prevent panics
		if stats == nil {
			fmt.Println("FastSync returned no stats (nil). The target peer may be unreachable or rejected the request.")
			os.Exit(1)
		}
		fmt.Printf("Sync completed in %dms\n", stats.TimeTaken)
		if stats.MainState == nil {
			fmt.Println("  Main DB TxID: unavailable (no state returned)")
		} else {
			fmt.Printf("  Main DB TxID: %d\n", stats.MainState.TxId)
		}
		if stats.AccountsState == nil {
			fmt.Println("  Accounts DB TxID: unavailable (no state returned)")
		} else {
			fmt.Printf("  Accounts DB TxID: %d\n", stats.AccountsState.TxId)
		}

	case "firstsync":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd firstsync <peer_multiaddr> <server|client>")
			os.Exit(1)
		}
		mode := args[1]
		if mode != "server" && mode != "client" {
			fmt.Println("Error: mode must be 'server' or 'client'")
			fmt.Println("Usage: jmdn -cmd firstsync <peer_multiaddr> <server|client>")
			os.Exit(1)
		}
		fmt.Printf("Starting first sync in %s mode...\n", mode)
		stats, err := client.FirstSync(args[0], mode)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		// Defensive guards against nil responses to prevent panics
		if stats == nil {
			fmt.Println("FirstSync returned no stats (nil). The target peer may be unreachable or rejected the request.")
			os.Exit(1)
		}
		fmt.Printf("Sync completed in %dms\n", stats.TimeTaken)
		if stats.MainState == nil {
			fmt.Println("  Main DB TxID: unavailable (no state returned)")
		} else {
			fmt.Printf("  Main DB TxID: %d\n", stats.MainState.TxId)
		}
		if stats.AccountsState == nil {
			fmt.Println("  Accounts DB TxID: unavailable (no state returned)")
		} else {
			fmt.Printf("  Accounts DB TxID: %d\n", stats.AccountsState.TxId)
		}

	case "sendfile":
		if len(args) < 3 {
			fmt.Println("Usage: jmdn -cmd sendfile <peer> <filepath> <remote_filename>")
			os.Exit(1)
		}
		resp, err := client.SendFile(args[0], args[1], args[2])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "ygg":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd ygg <target> <message>")
			os.Exit(1)
		}
		resp, err := client.SendYggdrasilMessage(args[0], strings.Join(args[1:], " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("\nAvailable commands:")
		fmt.Println("  help                 - Show this help message")
		fmt.Println("  listpeers, list      - List all managed peers")
		fmt.Println("  addrs                - Show node addresses")
		fmt.Println("  stats                - Show messaging statistics")
		fmt.Println("  dbstate              - Show database state")
		fmt.Println("  addpeer <addr>       - Add a peer")
		fmt.Println("  removepeer <id>     - Remove a peer")
		fmt.Println("  cleanpeers          - Clean offline peers")
		fmt.Println("  sendmsg <tgt> <msg>  - Send message via libp2p")
		fmt.Println("  ygg <tgt> <msg>      - Send message via Yggdrasil")
		fmt.Println("  sendfile <peer> <filepath> <remote> - Send file")
		fmt.Println("  broadcast <msg>      - Broadcast message")
		fmt.Println("  getdid <did>         - Get DID document")
		fmt.Println("  fastsync <peer>      - Fast sync with peer")
		fmt.Println("  firstsync <peer> <server|client> - First sync: get all data from peer (server) or receive all data (client)")
		os.Exit(1)
	}
}

func StartAPIServer(ctx context.Context, address string, enableExplorer bool) error {
	// Create ImmuDB API server
	server, err := explorer.NewImmuDBServer()
	if err != nil {
		return fmt.Errorf("failed to create ImmuDB API server: %w", err)
	}

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockPollerThread, func(ctx context.Context) error {
		explorer.StartBlockPoller(ctx, server, 7*time.Second)
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.BlockPollerThread).Msg("Failed to start GRO goroutine")
	}

	log.Info().Str("address", address).Msg("Starting ImmuDB API server")
	return server.StartWithContext(ctx, address)
}

// Update this function:
func startDIDServer(ctx context.Context, h host.Host, address string) error {
	didDBClient, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		//Debugging
		fmt.Println("Failed to get DID database client", err)

		log.Warn().Err(err).Msg("Failed to initialize DID propagation with ImmuDB. Starting in standalone mode.")
		// We'll continue with a standalone server
	} else {
		//Debugging
		// fmt.Println("Got DID database client successfully", didDBClient)

		log.Info().Msg("DID propagation initialized successfully")
	}
	// Start the DID server with our existing client
	return DID.StartDIDServerWithContext(ctx, h, address, didDBClient)
}

// initYggdrasilMessaging initializes the Yggdrasil messaging system
func initYggdrasilMessaging(ctx context.Context) {
	directMSG.StartYggdrasilListener(ctx)
	// Assign yggdraisl address to the config.Yggdrasil_Address

	fmt.Println(config.ColorGreen+"Yggdrasil messaging service started on port:"+config.ColorReset, directMSG.YggdrasilPort)
}

// Initialize main database connection pool
func initMainDBPool(enableLoki bool, username, password string) error {
	poolingConfig := &config.PoolingConfig{
		DBAddress:  config.DBAddress,
		DBPort:     config.DBPort,
		DBName:     config.DBName,
		DBUsername: username,
		DBPassword: password,
	}

	// Initialize the global pool
	config.InitGlobalPoolWithLoki(poolingConfig)
	mainDBPool = config.GetGlobalPool(context.Background())

	// Also initialize the DB_OPs main pool
	fmt.Println("Initializing DB_OPs main pool...")
	poolConfig := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPoolWithLoki(poolConfig, enableLoki, username, password); err != nil {
		return fmt.Errorf("failed to initialize DB_OPs main pool: %w", err)
	}
	fmt.Println("DB_OPs main pool initialized successfully")

	log.Info().Str("database", config.DBName).Msg("Main database connection pool initialized")
	return nil
}

// Initialize accounts database connection pool
func initAccountsDBPool(enableLoki bool, username, password string) error {
	// Use the DB_OPs package to initialize the accounts pool
	// This ensures the database exists and the pool is properly configured
	if err := DB_OPs.InitAccountsPool(); err != nil {
		return fmt.Errorf("failed to initialize accounts database pool: %w", err)
	}

	log.Info().Str("database", config.AccountsDBName).Msg("Accounts database connection pool initialized")
	return nil
}

// initFastSync initializes the FastSync service
func initFastSync(n *config.Node, mainClient *config.PooledConnection, accountsClient *config.PooledConnection, ionLogger *ion.Ion) *fastsync.FastSync {

	fs := fastsync.NewFastSync(n.Host, mainClient, accountsClient, ionLogger)
	log.Info().Msg("FastSync service initialized - will get connections when needed")
	return fs
}

// initPubSub initializes the PubSub system for the node
func initPubSub(n *config.Node) (*Pubsub.StructGossipPubSub, error) {
	fmt.Println("Initializing PubSub system...")

	// Create a protocol ID for PubSub (using the consensus channel name as protocol)
	pubSubProtocol := config.BuddyNodesMessageProtocol

	// Initialize the GossipPubSub instance
	gossipPubSub, err := Pubsub.NewGossipPubSub(n.Host, pubSubProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GossipPubSub: %w", err)
	}

	fmt.Printf("✅ PubSub system initialized successfully for host: %s\n", n.Host.ID())
	fmt.Printf("📡 PubSub protocol: %s\n", pubSubProtocol)

	return gossipPubSub, nil
}

func main() {
	// Command-line flags for node configuration
	seedNodeURL := flag.String("seednode", "", "Seed node gRPC URL for peer registration (e.g., localhost:9090)")
	peerAlias := flag.String("alias", "", "Peer alias for registration with seed node")
	enableLoki := flag.Bool("loki", false, "Enable Loki logging (default: false)")
	heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
	metricsPort := flag.String("metrics", "", "Port for Prometheus metrics (empty disables metrics server)")
	grotrack := flag.Bool("grotrack", false, "Track GRO goroutines in Prometheus/Grafana (requires -metrics)")
	enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
	apiPort := flag.Int("api", 0, "Run ImmuDB API on specified port (0 = disabled)")
	enableExplorer := flag.Bool("explorer", false, "Enable blockchain explorer UI (default: false)")
	blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
	blockgRPC := flag.Int("blockgrpc", 0, "Run Block gRPC server on specified port (0 = disabled)")
	cliGRPC := flag.Int("cli", 15053, "CLI gRPC server address")
	DIDgRPC := flag.String("did", "localhost:15052", "DID gRPC server address")
	gETHgRPC := flag.Int("geth", 15054, "gETH gRPC server address")
	gETHFacade := flag.Int("facade", 8545, "gETH Facade server address")
	gETHWSServer := flag.Int("ws", 8546, "gETH WSServer address")
	smartRPC := flag.Int("smart", 15056, "Smart Contract gRPC server address")
	chainID := flag.Int("chainID", 7000700, "Chain ID for the blockchain network")
	immudbUsername := flag.String("immudb-user", "immudb", "ImmuDB username")
	immudbPassword := flag.String("immudb-pass", "immudb", "ImmuDB password")

	command := flag.String("cmd", "", "Execute a CLI command (e.g., listpeers, addrs, stats, dbstate)")
	versionFlag := flag.Bool("version", false, "Print version information and exit")

	// Parse flags
	flag.Parse()

	// Set global ChainID for BlockProcessing (used by EVM)
	// This avoids passing chainID through every function call
	BlockProcessing.SetChainID(*chainID)

	// Exit immediately if version flag is set, before ANY initialization
	// This prevents any side effects from package imports or init() functions
	if *versionFlag {
		fmt.Println(version.String())
		return
	}

	// Initialize Global Go Routine Orchestrator first
	initGlobalGRO()
	initAppandLocalGRO()

	// Initialize messaging cleanup routines
	messaging.StartBlockPropagationCleanup()
	messaging.StartBroadcastCleanup()

	var nodeManager *node.NodeManager
	if err := ImmuDB_CA.EnsureTLSAssets(".immudb_state"); err != nil {
		fmt.Printf("Failed to ensure TLS assets: %v\n", err)
		log.Fatal()
	}
	// fmt.Println("ImmuDB TLS assets generated.")


	// Handle command execution mode - if -cmd is provided, execute command via gRPC and exit
	if *command != "" {
		runCommand(*command, flag.Args(), *cliGRPC)
		return
	}

	// Update the global seed node URL if provided via command-line
	if *seedNodeURL != "" {
		config.SetSeedNodeURL(*seedNodeURL)
	}

	// Initialize logger
	logFileName := "p2p-node.log"
	asyncLogger := logging.NewAsyncLogger()
	Logger, err := asyncLogger.NamedLogger("p2p-node", logFileName)
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}
	// Clean up any leftover temp files from a previous run
	defer Logger.Close()

	groTrackingEnabled = shouldEnableGROTracking(*grotrack, *metricsPort)

	// Start metrics server only when a metrics port is provided.
	// Default: http://localhost:8081/metrics (port set by -metrics flag).
	if *metricsPort != "" && *metricsPort != "0" {
		metricsAddr := ":" + *metricsPort
		metrics.StartMetricsServer(metricsAddr)
		fmt.Printf(
			config.ColorGreen+"\nMetrics available at "+config.ColorReset+"http://localhost%s/metrics\n",
			metricsAddr,
		)
	} else if *grotrack {
		log.Warn().Msg("grotrack enabled but metrics port is not set; GRO tracking disabled")
	}
	// Log version on startup
	log.Info().Str("version", version.String()).Msg("Starting JMDN node")

	// Create a cancellable context for clean shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.ShutdownThread, func(ctx context.Context) error {
		<-sigCh

		fmt.Println("\nShutdown signal received, closing connections...")
		cancel()
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.ShutdownThread).Msg("Failed to start GRO goroutine")
	}

	// Initialize database connection pools FIRST
	fmt.Println("Initializing main database pool...")
	if err := initMainDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize main database pool")
	}
	fmt.Println("Main database pool initialized successfully")

	if err := initAccountsDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize accounts database pool")
	}

	// =========================================================================
	// Smart Contract Service Integration
	// =========================================================================
	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, "SmartContractServer", func(ctx context.Context) error {
		return SmartContract.StartIntegratedServer(ctx, *smartRPC, *chainID, *gETHgRPC, *DIDgRPC, *blockgen)
	}); err != nil {
		log.Error().Err(err).Msg("Failed to start SmartContractServer goroutine")
	}

	// Discover Yggdrasil address BEFORE creating the node
	fmt.Println("Discovering Yggdrasil address...")
	ipv6, err := helper.GetTun0GlobalIPv6()
	if err != nil || ipv6 == "" {
		ipv6 = "?"
		log.Printf("Error getting Yggdrasil IPv6 address: %v", err)
	}
	config.Yggdrasil_Address = ipv6
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Address:"+config.ColorReset, ipv6)

	// Start the node
	fmt.Println("Creating libp2p node...")
	n, err := node.NewNode(ctx)
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()
	fmt.Println("Node created successfully")

	// Set the host instance for broadcast messaging
	messaging.SetHostInstance(n.Host)

	// Initialize the listener node (minimal listener for block propagation)
	// We no longer need Sequencer response handlers
	listener := MessagePassing.NewListenerNode(ctx, n.Host, nil)
	fmt.Printf("✅ Message listener initialized with ID: %s\n", listener.ListenerBuddyNode.PeerID.String())

	// Initialize PubSub system
	globalPubSub, err := initPubSub(n)
	if err != nil {
		fmt.Printf("Failed to initialize PubSub system: %v\n", err)
		log.Error().Err(err).Msg("Failed to initialize PubSub system")
		// Continue without PubSub - some features may be limited
	} else {
		fmt.Println("✅ PubSub system ready for consensus and messaging")
		log.Info().Msg("PubSub system initialized successfully")
		// Store reference for later use
		_ = globalPubSub // Mark as used to avoid linting error
	}

	// Set the stream handler for receiving files for fastsync. This is crucial
	// for the final phase of the sync process.
	n.Host.SetStreamHandler(config.FileProtocol, func(s network.Stream) {
		// Use an empty string for outputPath to use the default path in HandleFileStream
		transfer.HandleFileStream(s, "")
	})

	// Initialize database clients using the pools
	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get main database connection from pool")
	}
	defer func() {
		if mainDBClient != nil {
			DB_OPs.PutMainDBConnection(mainDBClient)
		}
	}()

	// Debugging
	// fmt.Println("Getting accounts database connection from pool")

	didDBClient, err := DB_OPs.GetAccountConnectionandPutBack(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get accounts database connection from pool")
	}

	// Debugging
	// fmt.Println("Got accounts database connection from pool", didDBClient)

	defer func() {
		if didDBClient != nil {
			DB_OPs.PutAccountsConnection(didDBClient)
		}
	}()

	// Initialize FastSync service
	fastSyncer = initFastSync(n, mainDBClient, didDBClient, Logger.GetNamedLogger())

	// Initialize Yggdrasil messaging if enabled
	if *enableYggdrasil {
		initYggdrasilMessaging(ctx)
		log.Info().Msgf("Yggdrasil messaging enabled on port %d", directMSG.YggdrasilPort)
	}

	// Display node identity
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Full Peer Address:"+config.ColorReset, "/ip6/"+config.Yggdrasil_Address+"/tcp/15000/p2p/"+n.Host.ID().String())

	fmt.Println(config.ColorGreen+"Node ID:"+config.ColorReset, n.Host.ID().String())
	fmt.Println("Addresses:")
	for _, addr := range n.Host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
	}

	// Initialize node manager
	nodeManager, err = node.NewNodeManagerWithLogger(n)
	if err != nil {
		fmt.Printf("Failed to initialize node manager: %v\n", err)
		return
	}
	// Debugging
	fmt.Println("Node manager initialized successfully")

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
	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.DIDThread, func(ctx context.Context) error {
		log.Info().Str("address", *DIDgRPC).Msg("Starting DID gRPC server")
		if err := startDIDServer(ctx, n.Host, *DIDgRPC); err != nil {
			fmt.Println("Failed to start DID gRPC server:", err)
			log.Error().Err(err).Msg("Failed to start DID gRPC server")
		}
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.DIDThread).Msg("Failed to start GRO goroutine")
	}

	if *blockgen > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockgenThread, func(ctx context.Context) error {
			log.Info().Msgf("Starting block generator on port %d", *blockgen)
			fmt.Printf("\nBlock generator available at http://localhost:%d\n", *blockgen)
			if err := Block.StartserverWithContext(ctx, "0.0.0.0", *blockgen, n.Host, *chainID); err != nil {
				log.Error().Err(err).Msg("Block generator server stopped")
			}
			return nil
		}); err != nil {
			log.Error().Err(err).Str("thread", GRO.BlockgenThread).Msg("Failed to start GRO goroutine")
		}
	}

	if *blockgRPC > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockgRPCThread, func(ctx context.Context) error {
			log.Info().Int("port", *blockgRPC).Msg("Starting block gRPC server")
			fmt.Printf("\nBlock gRPC server available at localhost:%d\n", *blockgRPC)
			if err := Block.StartGRPCServer(*blockgRPC, n.Host, *chainID); err != nil {
				log.Error().Err(err).Msg("Failed to start block gRPC server")
			}
			return nil
		}); err != nil {
			log.Error().Err(err).Str("thread", GRO.BlockgRPCThread).Msg("Failed to start GRO goroutine")
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

			// Perform neighbor discovery after successful registration
			fmt.Println("\n🔍 Starting neighbor discovery process...")
			err = seedClient.DiscoverAndAddNeighbors(n.Host, nodeManager)
			if err != nil {
				fmt.Printf("⚠️  Neighbor discovery failed: %v\n", err)
				log.Error().Err(err).Msg("Neighbor discovery failed")
			} else {
				fmt.Println("✅ Neighbor discovery completed successfully")
				log.Info().Msg("Neighbor discovery completed successfully")
			}
		}
	}

	if *gETHgRPC > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.GETHgRPCThread, func(ctx context.Context) error {
			fmt.Printf("Starting gETH gRPC server on port %d\n", *gETHgRPC)
			if err := gETH.StartGRPC(*gETHgRPC, *chainID); err != nil {
				log.Error().Err(err).Msg("gETH gRPC server error")
			}
			return nil
		}); err != nil {
			log.Error().Err(err).Str("thread", GRO.GETHgRPCThread).Msg("Failed to start GRO goroutine")
		}
	}

	if *apiPort > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.ExplorerThread, func(ctx context.Context) error {
			log.Info().Msgf("Starting ImmuDB API on port %d", *apiPort)
			fmt.Printf("\nImmuDB API available at http://localhost:%d/api\n", *apiPort)

			if *enableExplorer {
				fmt.Printf("🌐 Blockchain Explorer UI available at http://localhost:%d\n", *apiPort)
			} else {
				fmt.Printf("ℹ️  Blockchain Explorer UI disabled (use --explorer flag to enable)\n")
			}

			// Initialize API server
			apiAddr := fmt.Sprintf(":%d", *apiPort)
			if err := StartAPIServer(ctx, apiAddr, *enableExplorer); err != nil {
				log.Error().Err(err).Msg("Failed to start API server")
			}
			return nil
		}); err != nil {
			log.Error().Err(err).Str("thread", GRO.ExplorerThread).Msg("Failed to start GRO goroutine")
		}
	}

	cmdHandler := &cli.CommandHandler{
		Node:            n,
		NodeManager:     nodeManager,
		FastSyncer:      fastSyncer,
		SeedNode:        *seedNodeURL,
		EnableYggdrasil: *enableYggdrasil,
		ChainID:         *chainID,
		FacadePort:      *gETHFacade,
		WSPort:          *gETHWSServer,
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
		StartFacadeServer(*gETHFacade, *chainID, *smartRPC)
	}

	if *gETHWSServer > 0 {
		fmt.Printf("Starting gETH WSServer on port %d\n", *gETHWSServer)
		StartWSServer(*gETHWSServer, *chainID, *smartRPC)
	}

	// Start CLI without timeout - run indefinitely
	done := make(chan error, 1)
	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.CLIThread, func(ctx context.Context) error {
		if err := cmdHandler.StartCLI(ctx, "0.0.0.0", *cliGRPC); err != nil {
			done <- err
		}
		return nil
	}); err != nil {
		log.Error().Err(err).Str("thread", GRO.CLIThread).Msg("Failed to start GRO goroutine")
		done <- err
	}

	// Wait for CLI to complete or error
	if err := <-done; err != nil {
		log.Error().Err(err).Msg("Failed to start CLI")
	}
}
