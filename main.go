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

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/Block"
	"gossipnode/CA/ImmuDB_CA"
	cli "gossipnode/CLI"
	"gossipnode/DB_OPs"
	"gossipnode/DID"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer"
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
	fastSyncer   *fastsync.FastSync
	immuClient   *config.ImmuClient
	globalPubSub *Pubsub.StructGossipPubSub
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

func StartAPIServer(address string, enableExplorer bool) error {
	// Create ImmuDB API server
	server, err := explorer.NewImmuDBServer(enableExplorer)
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
		// fmt.Println("Got DID database client successfully", didDBClient)

		log.Info().Msg("DID propagation initialized successfully")
	}
	// Start the DID server with our existing client
	return DID.StartDIDServer(h, address, didDBClient)
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
	config.InitGlobalPoolWithLoki(poolingConfig, enableLoki)
	mainDBPool = config.GetGlobalPool()

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
	if err := DB_OPs.InitAccountsPoolWithLoki(enableLoki, username, password); err != nil {
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
	var nodeManager *node.NodeManager
	if err := ImmuDB_CA.EnsureTLSAssets(".immudb_state"); err != nil {
		fmt.Printf("Failed to ensure TLS assets: %v\n", err)
		log.Fatal()
	}
	fmt.Println("ImmuDB TLS assets generated.")

	// Command-line flags for node configuration
	seedNodeURL := flag.String("seednode", "", "Seed node gRPC URL for peer registration (e.g., localhost:9090)")
	peerAlias := flag.String("alias", "", "Peer alias for registration with seed node")
	enableLoki := flag.Bool("loki", false, "Enable Loki logging (default: false)")
	heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
	metricsPort := flag.String("metrics", "8081", "Port for Prometheus metrics")
	enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
	apiPort := flag.Int("api", 0, "Run ImmuDB API on specified port (0 = disabled)")
	enableExplorer := flag.Bool("explorer", false, "Enable blockchain explorer UI (default: false)")
	blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
	blockgRPC := flag.Int("blockgrpc", 0, "Run Block gRPC server on specified port (0 = disabled)")
	mempoolgRPC := flag.String("mempool", "localhost:15051", "Mempool gRPC server address")
	cliGRPC := flag.Int("cli", 15053, "CLI gRPC server address")
	DIDgRPC := flag.String("did", "localhost:15052", "DID gRPC server address")
	gETHgRPC := flag.Int("geth", 15054, "gETH gRPC server address")
	gETHFacade := flag.Int("facade", 8545, "gETH Facade server address")
	gETHWSServer := flag.Int("ws", 8546, "gETH WSServer address")
	chainID := flag.Int("chainID", 7000700, "Chain ID for the blockchain network")
	immudbUsername := flag.String("immudb-user", "immudb", "ImmuDB username")
	immudbPassword := flag.String("immudb-pass", "immudb", "ImmuDB password")
	command := flag.String("cmd", "", "Execute a CLI command (e.g., listpeers, addrs, stats, dbstate)")
	flag.Parse()

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
	if err := initMainDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize main database pool")
	}
	fmt.Println("Main database pool initialized successfully")

	if err := initAccountsDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize accounts database pool")
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
	n, err := node.NewNode()
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()
	fmt.Println("Node created successfully")

	// Set the host instance for broadcast messaging
	messaging.SetHostInstance(n.Host)

	// Initialize the listener node for handling submit message protocol
	// This sets up the SubmitMessageProtocol handler for vote submission
	listener := MessagePassing.NewListenerNode(n.Host, Sequencer.NewResponseHandler())
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
	// fmt.Println("Getting accounts database connection from pool")

	didDBClient, err := DB_OPs.GetAccountsConnection()
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
	fastSyncer = initFastSync(n, mainDBClient, didDBClient)

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

	if mempoolgRPC == nil {
		log.Printf("No mempool gRPC address provided; cannot proceed.")
		return
	}

	address := *mempoolgRPC
	if err := Block.InitMempoolClient(address); err != nil {
		log.Printf("Failed to connect to mempool: %v", err)
	}
	defer Block.CloseMempoolClient()

	// Initialize routing client to the same address as mempool
	_, err = Block.NewRoutingServiceClient(address)
	if err != nil {
		log.Printf("Failed to connect to routing service: %v", err)
	} else {
		log.Printf("Routing client initialized successfully")
	}

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

	if *blockgRPC > 0 {
		go func() {
			log.Info().Int("port", *blockgRPC).Msg("Starting block gRPC server")
			fmt.Printf("\nBlock gRPC server available at localhost:%d\n", *blockgRPC)
			if err := Block.StartGRPCServer(*blockgRPC, n.Host, *chainID); err != nil {
				log.Error().Err(err).Msg("Failed to start block gRPC server")
			}
		}()
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

			if *enableExplorer {
				fmt.Printf("🌐 Blockchain Explorer UI available at http://localhost:%d\n", *apiPort)
			} else {
				fmt.Printf("ℹ️  Blockchain Explorer UI disabled (use --explorer flag to enable)\n")
			}

			// Initialize API server
			apiAddr := fmt.Sprintf(":%d", *apiPort)
			if err := StartAPIServer(apiAddr, *enableExplorer); err != nil {
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
