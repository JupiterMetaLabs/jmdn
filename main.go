package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gossipnode/logging"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/seed"
	"gossipnode/SIM"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"
)

func printDashes() {
	fmt.Println("\n", strings.Repeat("-", 50), "\n")
}

// initYggdrasilMessaging initializes the Yggdrasil messaging system
func initYggdrasilMessaging(ctx context.Context) {
	// Start the Yggdrasil listener
	directMSG.StartYggdrasilListener(ctx)
	fmt.Printf("Yggdrasil messaging service started on port %d\n", directMSG.YggdrasilPort)
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

	// Initialize Yggdrasil messaging if enabled
	if *enableYggdrasil {
		initYggdrasilMessaging(ctx)
		log.Info().Msgf("Yggdrasil messaging enabled on port %d", directMSG.YggdrasilPort)
	}

	// Display node identity
	fmt.Printf("Node ID: %s\n", n.Host.ID().String())
	fmt.Println("Addresses:")
	for _, addr := range n.Host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
	}

	// Start metrics server (just once)
	metricsAddr := ":" + *metricsPort
	metrics.StartMetricsServer(metricsAddr)
	fmt.Printf("\nMetrics available at http://localhost%s/metrics\n", metricsAddr)

	// Initialize node manager
	nodeManager, err = node.NewNodeManager(n)
	if err != nil {
		fmt.Printf("Failed to initialize node manager: %v\n", err)
		return
	}
	nodeManager.StartHeartbeat(*heartbeatInterval)
	defer nodeManager.Shutdown()

    SIM.StartBlockSimulation(n.Host, 10*time.Second, 30*time.Second)

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

	fmt.Println("\nCommands:")
	fmt.Println("  msg <peer_multiaddr> <message>  - Send a message to a peer via libp2p")
	fmt.Println("  ygg <peer_multiaddr|ygg_ipv6> <message> - Send a message using Yggdrasil")
	fmt.Println("  file <peer_multiaddr> <filepath> - Send a file to a peer")
	fmt.Println("  addpeer <peer_multiaddr> - Add a peer to managed nodes")
	fmt.Println("  removepeer <peer_id> - Remove a peer from managed nodes")
	fmt.Println("  listpeers - Show all managed peers")
	fmt.Println("  peers - Request updated peer list from seed")
	fmt.Println("  stats - Show messaging statistics")
	fmt.Println("  broadcast <message> - Broadcast a message to all connected peers")
	fmt.Println("  exit - Exit the program")

	var wg sync.WaitGroup
	wg.Add(1)

	// Command-line input loop
	go func() {
		defer wg.Done()
		defer fmt.Println("Exiting...")
		fmt.Println()
		scanner := bufio.NewScanner(os.Stdin)
		fmt.Print(">>> ")
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
				// defer printDashes()
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
				// defer printDashes()
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

			default:
				fmt.Println("Unknown command")
			}

			fmt.Print(">>> ")
		}
	}()

	wg.Wait()
}
