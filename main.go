// package main

// import (
// 	"bufio"
// 	"fmt"
// 	"os"
// 	"strings"
// 	"sync"

// 	"gossipnode/node"
// )

// // Main function with simple CLI
// func main() {
// 	n, err := node.NewNode()
// 	if err != nil {
// 		fmt.Println("Error starting node:", err)
// 		return
// 	}
// 	defer n.Host.Close()

// 	fmt.Println("Commands: 'msg <peer_multiaddr> <message>' or 'file <peer_multiaddr> <filepath>' or 'exit'")

// 	var wg sync.WaitGroup
// 	wg.Add(1)

// 	// Command-line input loop
// 	go func() {
// 		defer wg.Done()
// 		scanner := bufio.NewScanner(os.Stdin)
// 		for scanner.Scan() {
// 			input := strings.TrimSpace(scanner.Text())
// 			if input == "exit" {
// 				return
// 			}

// 			parts := strings.SplitN(input, " ", 3)
// 			if len(parts) < 2 {
// 				fmt.Println("Invalid command")
// 				continue
// 			}

// 			switch parts[0] {
// 			case "msg":
// 				if len(parts) != 3 {
// 					fmt.Println("Usage: msg <peer_multiaddr> <message>")
// 					continue
// 				}
// 				err := node.SendMessage(n, parts[1], parts[2])
// 				if err != nil {
// 					fmt.Println("Error:", err)
// 				}
// 			case "file":
// 				if len(parts) != 3 {
// 					fmt.Println("Usage: file <peer_multiaddr> <filepath>")
// 					continue
// 				}
// 				err := node.SendFile(n, parts[1], parts[2])
// 				if err != nil {
// 					fmt.Println("Error:", err)
// 				}
// 			default:
// 				fmt.Println("Unknown command")
// 			}
// 		}
// 	}()

//		wg.Wait()
//	}
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/seed"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	var nodeManager *node.NodeManager

    // Command-line flags for node configuration
    isSeed := flag.Bool("seed", false, "Run as a seed node")
    connect := flag.String("connect", "", "Connect to a seed node (multiaddr)")
    heartbeatInterval := flag.Int("heartbeat", 300, "Heartbeat interval in seconds (default: 300)")
    metricsPort := flag.String("metrics", "8080", "Port for Prometheus metrics")
    flag.Parse()

    // Start the node
    n, err := node.NewNode()
    if err != nil {
        fmt.Println("Error starting node:", err)
        return
    }
    defer n.Host.Close()
    
    // Display node identity
    fmt.Printf("Node ID: %s\n", n.Host.ID().String())
    fmt.Println("Addresses:")
    for _, addr := range n.Host.Addrs() {
        fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
    }

    // Start metrics server (just once)
    metricsAddr := ":" + *metricsPort
    metrics.StartMetricsServer(metricsAddr)
    fmt.Printf("Metrics available at http://localhost%s/metrics\n", metricsAddr)


    // Initialize node manager
    nodeManager, err = node.NewNodeManager(n)
    if err != nil {
        fmt.Printf("Failed to initialize node manager: %v\n", err)
        return
    }
    nodeManager.StartHeartbeat(*heartbeatInterval)
    defer nodeManager.Shutdown()
    fmt.Printf("Node manager started with %d second heartbeat interval\n", *heartbeatInterval)


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
    fmt.Println("  msg <peer_multiaddr> <message>  - Send a message to a peer")
    fmt.Println("  file <peer_multiaddr> <filepath> - Send a file to a peer")
    fmt.Println("  addpeer <peer_multiaddr> - Add a peer to managed nodes")
    fmt.Println("  removepeer <peer_id> - Remove a peer from managed nodes")
    fmt.Println("  listpeers - Show all managed peers")
    fmt.Println("  peers - Request updated peer list from seed")
    fmt.Println("  exit - Exit the program")

    var wg sync.WaitGroup
    wg.Add(1)

    // Command-line input loop
    go func() {
        defer wg.Done()
        scanner := bufio.NewScanner(os.Stdin)
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

            default:
                fmt.Println("Unknown command")
            }
        }
    }()

    wg.Wait()
}