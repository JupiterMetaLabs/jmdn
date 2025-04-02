package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
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
	"gossipnode/seed"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/mattn/go-sqlite3"
	ma "github.com/multiformats/go-multiaddr"
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
func initFastSync(n *config.Node, client *config.ImmuClient) *fastsync.FastSync {
    fs := fastsync.NewFastSync(n.Host, client)
    log.Info().Msg("FastSync service initialized")
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
    logFileName := fmt.Sprintf("p2p-node-%s.log", time.Now().Format("2006-01-02"))
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
    fastSyncer = initFastSync(n, immuClient)

    // Initialize Yggdrasil messaging if enabled
    if *enableYggdrasil {
        initYggdrasilMessaging(ctx)
        log.Info().Msgf("Yggdrasil messaging enabled on port %d", directMSG.YggdrasilPort)
    }

    // Display node identity
    ipv6, err := helper.GetTun0GlobalIPv6()
    if err != nil {
        log.Printf("Error getting tun0 IPv6 address: %v", err)
    }
    fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Address:"+config.ColorReset, ipv6)
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

    fmt.Println("\n" + config.ColorCyan + "Available Commands:" + config.ColorReset)
    fmt.Println("  msg <peer_multiaddr> <message>   - Send a message to a peer via libp2p")
    fmt.Println("  ygg <peer_multiaddr|ygg_ipv6> <message> - Send a message using Yggdrasil")
    fmt.Println("  file <peer_multiaddr> <filepath> - Send a file to a peer")
    fmt.Println("  addpeer <peer_multiaddr>         - Add a peer to managed nodes")
    fmt.Println("  removepeer <peer_id>             - Remove a peer from managed nodes")
    fmt.Println("  listpeers                         - Show all managed peers")
    fmt.Println("  peers                             - Request updated peer list from seed")
    fmt.Println("  stats                             - Show messaging statistics")
    fmt.Println("  broadcast <message>              - Broadcast a message to all connected peers")
    fmt.Println("  fastsync <peer_multiaddr>        - Fast sync blockchain data with a peer")
    fmt.Println("  dbstate                           - Show current ImmuDB database state")
    fmt.Println("  propagateDID <did> <public_key>  - Propagate a DID to the network")
    fmt.Println("  getDID <did>                      - Get a DID document from the network")
    fmt.Println("  exit                              - Exit the program\n")


    var wg sync.WaitGroup
    wg.Add(1)

    // Command-line input loop
    go func() {
        defer wg.Done()
        defer fmt.Println("Exiting...")
        fmt.Println()
        scanner := bufio.NewScanner(os.Stdin)
        printPrompt()
        for scanner.Scan() {
            input := strings.TrimSpace(scanner.Text())
            if input == "exit" {
                return
            }

            parts := strings.SplitN(input, " ", 3)
            if len(parts) == 0 {
                continue
            }

            switch parts[0] {
            case "msg":
                if len(parts) != 3 {
                    fmt.Println("Usage: msg <peer_multiaddr> <message>")
                    continue
                }
                err := node.SendMessage(n, parts[1], parts[2])
                if err != nil {
                    fmt.Println("Error:", err)
                } else {
                    fmt.Println("Message sent successfully")
                }

            case "ygg":
                if !*enableYggdrasil {
                    fmt.Println("Yggdrasil messaging is disabled. Start with -ygg flag to enable.")
                    continue
                }
                if len(parts) != 3 {
                    fmt.Println("Usage: ygg <peer_multiaddr|ygg_ipv6> <message>")
                    continue
                }
                err := directMSG.SendYggdrasilMessage(parts[1], parts[2])
                if err != nil {
                    fmt.Println("Error sending via Yggdrasil:", err)
                }

            case "file":
                if len(parts) != 3 {
                    fmt.Println("Usage: file <peer_multiaddr> <filepath>")
                    continue
                }
                err := node.SendFile(n, parts[1], parts[2])
                if err != nil {
                    fmt.Println("Error:", err)
                } else {
                    fmt.Println("File sent successfully")
                }

            case "peers":
                if *connect == "" {
                    fmt.Println("No seed node specified. Use -connect flag to specify a seed node.")
                    continue
                }

                peers, err := seed.RequestPeers(n.Host, *connect, 20, "")
                if err != nil {
                    fmt.Printf("Error connecting to seed: %v\n", err)
                } else {
                    fmt.Printf("Connected to seed. Discovered %d peers\n", len(peers))
                    for i, p := range peers {
                        fmt.Printf("  %d. ID: %s, Addresses: %v\n", i+1, p.ID, p.Addrs)
                    }
                }

            case "addpeer":
                if len(parts) != 2 {
                    fmt.Println("Usage: addpeer <peer_multiaddr>")
                    continue
                }
                err := nodeManager.AddPeer(parts[1])
                if err != nil {
                    fmt.Printf("Failed to add peer: %v\n", err)
                } else {
                    fmt.Println("Peer added successfully and will be included in heartbeat cycles")
                }

            case "removepeer":
                if len(parts) != 2 {
                    fmt.Println("Usage: removepeer <peer_id>")
                    continue
                }
                err := nodeManager.RemovePeer(parts[1])
                if err != nil {
                    fmt.Printf("Failed to remove peer: %v\n", err)
                } else {
                    fmt.Println("Peer removed successfully from management")
                }

            case "listpeers":
                peers := nodeManager.ListManagedPeers()
                fmt.Printf("Managed peers (%d):\n", len(peers))
                for i, p := range peers {
                    status := "ONLINE"
                    if !p.IsAlive {
                        status = "OFFLINE"
                    }
                    lastSeen := time.Unix(p.LastSeen, 0).Format(time.RFC3339)
                    fmt.Printf("%d. ID: %s\n   Address: %s\n   Status: %s\n   Last seen: %s\n   Failures: %d\n",
                        i+1, p.ID, p.Multiaddr, status, lastSeen, p.HeartbeatFail)
                }
                printDashes()

            case "cleanpeers":
                cleaned, err := nodeManager.CleanupOfflinePeers(9) // Remove peers with 9+ failures
                if err != nil {
                    fmt.Printf("Error cleaning up peers: %v\n", err)
                } else {
                    fmt.Printf("Cleaned up %d offline peers\n", cleaned)
                }

            case "stats":
                if *enableYggdrasil {
                    stats := directMSG.GetMetrics()
                    fmt.Println("Yggdrasil Messaging Statistics:")
                    fmt.Printf("  Messages sent: %d\n", stats["messages_sent"])
                    fmt.Printf("  Messages received: %d\n", stats["messages_received"])
                    fmt.Printf("  Failed messages: %d\n", stats["messages_failed"])
                    printDashes()
                } else {
                    fmt.Println("Yggdrasil messaging is disabled.")
                }
            
            case "broadcast":
                if len(parts) < 2 {
                    fmt.Println("Usage: broadcast <message>")
                    continue
                }
                // Join all remaining parts as the message
                message := strings.Join(parts[1:], " ")
                err := node.BroadcastMessage(n, message)
                if err != nil {
                    fmt.Printf("Broadcast failed: %v\n", err)
                } else {
                    fmt.Println("Message broadcast initiated")
                }

            case "fastsync":
                if len(parts) != 2 {
                    fmt.Println("Usage: fastsync <peer_multiaddr>")
                    continue
                }
                
                // Parse the multiaddr
                addr, err := ma.NewMultiaddr(parts[1])
                if err != nil {
                    fmt.Printf("Invalid multiaddress: %v\n", err)
                    continue
                }
                
                // Extract peer ID from multiaddr
                addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
                if err != nil {
                    fmt.Printf("Failed to extract peer info: %v\n", err)
                    continue
                }
                
                // Get our database state before sync
                state, err := DB_OPs.GetDatabaseState(immuClient)
                if err != nil {
                    fmt.Printf("Failed to get database state: %v\n", err)
                    continue
                }
                
                fmt.Printf("Starting blockchain sync with peer %s\n", addrInfo.ID.String())
                fmt.Printf("Our current state: TxID=%d, Root=%x\n", state.TxId, state.TxHash)
                
                // Start the sync process
                startTime := time.Now()

                // err = fastSyncer.StartSync(addrInfo.ID)
                // if err != nil {
                //     fmt.Printf("Sync failed: %v\n", err)
                //     continue
                // }

                maxRetries := 3
                var syncErr error
                
                for retry := 0; retry < maxRetries; retry++ {
                    if retry > 0 {
                        fmt.Printf("Retry %d/%d after error: %v\n", retry+1, maxRetries, syncErr)
                        time.Sleep(2 * time.Second)
                    }
                    
                    syncErr = fastSyncer.StartSync(addrInfo.ID)
                    if syncErr == nil {
                        break
                    }
                }
                
                if syncErr != nil {
                    fmt.Printf("Sync failed after %d attempts: %v\n", maxRetries, syncErr)
                    continue
                }
                // Get post-sync state
                newState, err := DB_OPs.GetDatabaseState(immuClient)
                if err != nil {
                    fmt.Printf("Failed to get database state after sync: %v\n", err)
                    continue
                }
                
                fmt.Printf("Sync completed in %v\n", time.Since(startTime))
                fmt.Printf("New state: TxID=%d, Root=%x\n", newState.TxId, newState.TxHash)
                printDashes()
            
            case "propagateDID":
                if len(parts) != 3 {
                    fmt.Println("Usage: propagateDID <did> <public_key>")
                    continue
                }
                did := parts[1]
                publicKey := parts[2]
                
                fmt.Printf("Propagating DID %s with public key %s to the network...\n", did, publicKey)
                
                err := messaging.PropagateDID(n.Host, did, publicKey)
                if err != nil {
                    fmt.Printf("Failed to propagate DID: %v\n", err)
                } else {
                    fmt.Println("DID propagated successfully to all connected peers")
                }
            
            case "getDID":
                if len(parts) != 2 {
                    fmt.Println("Usage: getDID <did>")
                    continue
                }
                did := parts[1]
                
                doc, err := messaging.GetDID(did)
                if err != nil {
                    fmt.Printf("Failed to retrieve DID %s: %v\n", did, err)
                    continue
                }
                
                fmt.Println("DID Document:")
                fmt.Printf("  DID: %s\n", doc.DID)
                fmt.Printf("  Public Key: %s\n", doc.PublicKey)
                fmt.Printf("  Balance: %s\n", doc.Balance)
                fmt.Printf("  Created: %s\n", time.Unix(doc.CreatedAt, 0).Format(time.RFC3339))
                fmt.Printf("  Updated: %s\n", time.Unix(doc.UpdatedAt, 0).Format(time.RFC3339))

			case "dbstate":
				state, err := DB_OPs.GetDatabaseState(immuClient)
				if err != nil {
					fmt.Printf("Failed to get database state: %v\n", err)
					continue
				}
				
				fmt.Println("Current ImmuDB State:")
				fmt.Printf("  Transaction ID: %d\n", state.TxId)
				fmt.Printf("  Merkle Root: %x\n", state.TxHash)
				
				// Count entries in the database using pagination
				const maxKeysPerBatch = 2000 // Staying well under the 2500 limit
				var totalKeys int
				var lastKey string
				var hasMoreKeys = true
				
				for hasMoreKeys {
					keys, err := DB_OPs.GetKeys(immuClient,lastKey, maxKeysPerBatch)
					if err != nil {
						fmt.Printf("Failed to count database entries: %v\n", err)
						hasMoreKeys = false
						continue
					}
					
					count := len(keys)
					totalKeys += count 
					
					// If we got fewer keys than our limit, we've reached the end
					if count < maxKeysPerBatch {
						hasMoreKeys = false
					} else if count > 0 {
						// Set the last key for the next batch
						lastKey = keys[count-1]
					} else {
						hasMoreKeys = false
					}
				}
				
				fmt.Printf("  Total Keys: %d\n", totalKeys)
				printDashes()
                
            default:
                fmt.Println("Unknown command")
            }

            printPrompt()
        }
    }()

    wg.Wait()
}