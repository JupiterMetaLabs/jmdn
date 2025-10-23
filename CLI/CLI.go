package CLI

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/fastsync"
	"gossipnode/messaging"
	"gossipnode/messaging/directMSG"
	"gossipnode/node"
	"gossipnode/seednode"
	peerpb "gossipnode/seednode/proto"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// CommandHandler holds dependencies for CLI command execution
type CommandHandler struct {
	Node            *config.Node
	NodeManager     *node.NodeManager
	FastSyncer      *fastsync.FastSync
	MainClient      *config.PooledConnection
	DIDClient       *config.PooledConnection
	SeedNode        string
	EnableYggdrasil bool
	ChainID         int
	FacadePort      int
	WSPort          int
}

// Simple helper to print the CLI prompt in color
func printPrompt() {
	fmt.Printf(config.ColorGreen + ">>> " + config.ColorReset)
}

func printAddrs(n *config.Node) {
	for _, addr := range n.Host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
	}
}

func printDashes() {
	fmt.Println("\n" + strings.Repeat("-", 50) + "\n")
}

func PrintFuncs() {
	fmt.Println("\n" + config.ColorCyan + "Available Commands:" + config.ColorReset)
	fmt.Println("  help                             - Show this help message")
	fmt.Println("  addrs                            - Current Peer Addresses")
	fmt.Println("  msg <peer_multiaddr> <message>   - Send a message to a peer via libp2p")
	fmt.Println("  ygg <peer_multiaddr|ygg_ipv6> <message> - Send a message using Yggdrasil")
	fmt.Println("  file <peer_multiaddr> <filepath> <remote-filename> - Send a file to a peer")
	fmt.Println("  addpeer <peer_multiaddr>         - Add a peer to managed nodes")
	fmt.Println("  removepeer <peer_id>             - Remove a peer from managed nodes")
	fmt.Println("  listpeers                         - Show all managed peers")
	fmt.Println("  listaliases                       - List all peer aliases from seed node")
	fmt.Println("  discoverneighbors                 - Discover and add neighbors from seed node")
	fmt.Println("  seednodeStats                     - Check seed node connection and get peer statistics")
	fmt.Println("  stats                             - Show messaging statistics")
	fmt.Println("  broadcast <message>              - Broadcast a message to all connected peers")
	fmt.Println("  fastsync <peer_multiaddr>        - Fast sync blockchain data with a peer")
	fmt.Println("  dbstate                           - Show current ImmuDB database state")
	fmt.Println("  propagateDID <did> <public_key>  - Propagate a DID to the network")
	fmt.Println("  getDID <did>                      - Get a DID document from the network")
	fmt.Println("  syncinfo                          - Show FastSync configuration")
	fmt.Println("  gethstatus                        - Show gETH server status (chain ID, ports)")
	fmt.Println("  exit                              - Exit the program")
	printDashes()
}

func (h *CommandHandler) StartCLI(grpcPort int) error {
	PrintFuncs()
	fmt.Printf("Starting CLI with gRPC port: %d\n", grpcPort)

	// Create a context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to signal when we should exit
	exitChan := make(chan struct{})

	// Start gRPC server
	go func() {
		fmt.Printf("Starting gRPC server on port %d...\n", grpcPort)
		log.Println("Starting gRPC server on port ", grpcPort)
		if err := StartGRPCServer(h, grpcPort); err != nil {
			log.Printf("gRPC server error: %v", err)
		}
	}()

	// Command-line input loop
	go func() {
		defer func() {
			fmt.Println("Exiting...")
			close(exitChan)
		}()

		fmt.Println()
		scanner := bufio.NewScanner(os.Stdin)
		printPrompt()
		for scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "exit" {
				return
			}

			parts := strings.SplitN(input, " ", 4)
			if len(parts) == 0 {
				continue
			}

			h.handleCommand(parts)
			printPrompt()
		}
	}()

	// Wait for exit signal
	select {
	case <-exitChan:
		fmt.Println("CLI shutdown complete")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleCommand processes a single command
func (h *CommandHandler) handleCommand(parts []string) {
	switch parts[0] {
	case "addrs":
		printAddrs(h.Node)
	case "help":
		PrintFuncs()
	case "msg":
		h.handleSendMessage(parts)
	case "ygg":
		h.handleYggdrasilMessage(parts)
	case "file":
		h.handleSendFile(parts)
	case "seednodeStats":
		h.handleSeedNodeStats(parts)
	case "addpeer":
		h.handleAddPeer(parts)
	case "removepeer":
		h.handleRemovePeer(parts)
	case "listpeers":
		h.handleListPeers()
	case "listaliases":
		h.handleListAliases()
	case "discoverneighbors":
		h.handleDiscoverNeighbors()
	case "cleanpeers":
		h.handleCleanPeers()
	case "stats":
		h.handleShowStats()
	case "broadcast":
		h.handleBroadcast(parts)
	case "fastsync":
		h.handleFastSync(parts)
	case "propagateDID":
		h.handlePropagateDID(parts)
	case "syncinfo":
		h.handleSyncInfo()
	case "getDID":
		h.handleGetDID(parts)
	case "dbstate":
		h.handleDBState()
	case "gethstatus":
		h.handleGethStatus()
	default:
		fmt.Println("Unknown command")
	}
}

// Individual command handlers
func (h *CommandHandler) handleSendMessage(parts []string) {
	if len(parts) != 3 {
		fmt.Println("Usage: msg <peer_multiaddr> <message>")
		return
	}
	err := node.SendMessage(h.Node, parts[1], parts[2])
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Println("Message sent successfully")
}

func (h *CommandHandler) handleYggdrasilMessage(parts []string) {
	if !h.EnableYggdrasil {
		fmt.Println("Yggdrasil messaging is disabled. Start with -ygg flag to enable.")
		return
	}
	if len(parts) != 3 {
		fmt.Println("Usage: ygg <peer_multiaddr|ygg_ipv6> <message>")
		return
	}
	err := directMSG.SendYggdrasilMessage(parts[1], parts[2])
	if err != nil {
		fmt.Println("Error sending via Yggdrasil:", err)
	}
}

func (h *CommandHandler) handleSendFile(parts []string) {
	// Debugging the parts
	fmt.Println("Parts:", parts)
	if len(parts) < 3 {
		fmt.Println("Usage: file <peer_multiaddr> <filepath> [remote_filename]")
		return
	}

	// Set default remote filename if not provided
	remoteFilename := ""
	if len(parts) >= 4 {
		remoteFilename = parts[3]
	}

	err := node.SendFile(h.Node, parts[1], parts[2], remoteFilename)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Println("File sent successfully")
}

func (h *CommandHandler) handleSeedNodeStats(parts []string) {
	if h.SeedNode == "" {
		fmt.Println("❌ No seed node specified. Use -seednode flag to specify a seed node.")
		return
	}

	fmt.Printf("🔍 Checking seed node connection: %s\n", h.SeedNode)
	printDashes()

	// Create seed node client to test connection
	client, err := seednode.NewClient(h.SeedNode)
	if err != nil {
		fmt.Printf("❌ Failed to connect to seed node: %v\n", err)
		fmt.Println("💡 Check if the seed node is running and accessible.")
		return
	}
	defer client.Close()

	fmt.Println("✅ Successfully connected to seed node!")

	// Test latency with multiple ping attempts
	fmt.Println("\n🏓 Testing network latency...")
	latencyStats := h.measureLatency(client)

	// Test health check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.HealthCheck(ctx)
	if err != nil {
		fmt.Printf("⚠️  Seed node health check failed: %v\n", err)
	} else {
		fmt.Println("✅ Seed node health check passed!")
	}

	// Get peer statistics
	fmt.Println("\n📊 Fetching peer statistics...")

	request := &peerpb.PeerListRequest{
		Limit:  1000,
		Status: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
	}

	response, err := client.ListPeers(ctx, request)
	if err != nil {
		fmt.Printf("❌ Failed to get peer list: %v\n", err)
		return
	}

	// Display statistics
	fmt.Printf("📈 Seed Node Statistics:\n")
	fmt.Printf("  Total Active Peers: %d\n", len(response.Peers))

	// Count peers with aliases (this is an approximation)
	aliasCount := 0
	for _, peer := range response.Peers {
		// Try to get alias for this peer (this is a workaround)
		_, aliasErr := client.GetPeerByAlias(peer.PeerId)
		if aliasErr == nil {
			aliasCount++
		}
	}

	fmt.Printf("  Peers with Aliases: %d\n", aliasCount)
	fmt.Printf("  Peers without Aliases: %d\n", len(response.Peers)-aliasCount)

	// Display latency statistics
	fmt.Printf("\n🌐 Network Latency Statistics:\n")
	fmt.Printf("  Average Latency: %.2f ms\n", latencyStats.Average)
	fmt.Printf("  Minimum Latency: %.2f ms\n", latencyStats.Minimum)
	fmt.Printf("  Maximum Latency: %.2f ms\n", latencyStats.Maximum)
	fmt.Printf("  Successful Pings: %d/%d\n", latencyStats.Successful, latencyStats.Total)
	if latencyStats.Successful > 0 {
		fmt.Printf("  Packet Loss: %.1f%%\n", float64(latencyStats.Total-latencyStats.Successful)/float64(latencyStats.Total)*100)
	}

	// Show recent peers (first 10)
	if len(response.Peers) > 0 {
		fmt.Printf("\n📋 Recent Peers (showing first 10):\n")
		fmt.Println(strings.Repeat("-", 80))
		fmt.Printf("%-20s %-50s %-10s\n", "Peer ID", "Status", "Seq")
		fmt.Println(strings.Repeat("-", 80))

		displayCount := 10
		if len(response.Peers) < displayCount {
			displayCount = len(response.Peers)
		}

		for i := 0; i < displayCount; i++ {
			peer := response.Peers[i]
			peerID := peer.PeerId
			if len(peerID) > 20 {
				peerID = peerID[:20] + "..."
			}

			fmt.Printf("%-20s %-50s %-10d\n",
				peerID,
				peer.CurrentStatus.String(),
				peer.Seq)
		}

		if len(response.Peers) > displayCount {
			fmt.Printf("... and %d more peers\n", len(response.Peers)-displayCount)
		}
		fmt.Println(strings.Repeat("-", 80))
	}

	fmt.Println("\n🎯 Seed node connection is healthy and operational!")
	printDashes()
	fmt.Println("✅ seednodeStats command completed successfully!")
}

func (h *CommandHandler) handleAddPeer(parts []string) {
	if len(parts) != 2 {
		fmt.Println("Usage: addpeer <peer_multiaddr>")
		return
	}
	err := h.NodeManager.AddPeer(parts[1])
	if err != nil {
		fmt.Printf("Failed to add peer: %v\n", err)
	} else {
		fmt.Println("Peer added successfully and will be included in heartbeat cycles")
	}
}

func (h *CommandHandler) handleRemovePeer(parts []string) {
	if len(parts) != 2 {
		fmt.Println("Usage: removepeer <peer_id>")
		return
	}
	err := h.NodeManager.RemovePeer(parts[1])
	if err != nil {
		fmt.Printf("Failed to remove peer: %v\n", err)
	} else {
		fmt.Println("Peer removed successfully from management")
	}
}

func (h *CommandHandler) handleListPeers() {
	peers := h.NodeManager.ListManagedPeers()
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
}

func (h *CommandHandler) handleCleanPeers() {
	cleaned, err := h.NodeManager.CleanupOfflinePeers(9) // Remove peers with 9+ failures
	if err != nil {
		fmt.Printf("Error cleaning up peers: %v\n", err)
	} else {
		fmt.Printf("Cleaned up %d offline peers\n", cleaned)
	}
}

func (h *CommandHandler) handleShowStats() {
	if h.EnableYggdrasil {
		stats := directMSG.GetMetrics()
		fmt.Println("Yggdrasil Messaging Statistics:")
		fmt.Printf("  Messages sent: %d\n", stats["messages_sent"])
		fmt.Printf("  Messages received: %d\n", stats["messages_received"])
		fmt.Printf("  Failed messages: %d\n", stats["messages_failed"])
		printDashes()
	} else {
		fmt.Println("Yggdrasil messaging is disabled.")
	}
}

func (h *CommandHandler) handleBroadcast(parts []string) {
	if len(parts) < 2 {
		fmt.Println("Usage: broadcast <message>")
		return
	}
	// Join all remaining parts as the message
	message := strings.Join(parts[1:], " ")
	err := node.BroadcastMessage(h.Node, message)
	if err != nil {
		fmt.Printf("Broadcast failed: %v\n", err)
	} else {
		fmt.Println("Message broadcast initiated")
	}
}

func (h *CommandHandler) handleFastSync(parts []string) {
	if len(parts) != 2 {
		fmt.Println("Usage: fastsync <peer_multiaddr>")
		return
	}

	err := h.checkDBClient()
	if err != nil {
		fmt.Printf("Database client not initialized: %v\n", err)
		return
	}

	err = h.checkDIDClient()
	if err != nil {
		fmt.Printf("DID database client not initialized: %v\n", err)
		return
	}

	// Parse the multiaddr
	addr, err := ma.NewMultiaddr(parts[1])
	if err != nil {
		fmt.Printf("Invalid multiaddress: %v\n", err)
		return
	}

	// Extract peer ID from multiaddr
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		fmt.Printf("Failed to extract peer info: %v\n", err)
		return
	}

	// Get both database states before sync
	mainState, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		fmt.Printf("Failed to get main database state: %v\n", err)
		return
	}

	accountsState, err := DB_OPs.GetDatabaseState(h.DIDClient.Client)
	if err != nil {
		fmt.Printf("Failed to get accounts database state: %v\n", err)
		return
	}

	fmt.Printf("Starting blockchain sync with peer %s\n", addrInfo.ID.String())
	fmt.Printf("Our current main DB state: TxID=%d, Root=%x\n", mainState.TxId, mainState.TxHash)
	fmt.Printf("Our current accounts DB state: TxID=%d, Root=%x\n", accountsState.TxId, accountsState.TxHash)

	// Start the sync process
	startTime := time.Now()

	maxRetries := 3
	var syncErr error

	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			fmt.Printf("Retry %d/%d after error: %v\n", retry+1, maxRetries, syncErr)
			time.Sleep(2 * time.Second)
		}

		_, syncErr = h.FastSyncer.HandleSync(addrInfo.ID)
		if syncErr == nil {
			break
		}
	}

	if syncErr != nil {
		fmt.Printf("Sync failed after %d attempts: %v\n", maxRetries, syncErr)
		return
	}

	// Get post-sync states
	newMainState, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		fmt.Printf("Failed to get main database state after sync: %v\n", err)
		return
	}

	newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient.Client)
	if err != nil {
		fmt.Printf("Failed to get accounts database state after sync: %v\n", err)
		return
	}

	fmt.Printf("Sync completed in %v\n", time.Since(startTime))
	fmt.Printf("New main DB state: TxID=%d, Root=%x\n", newMainState.TxId, newMainState.TxHash)
	fmt.Printf("New accounts DB state: TxID=%d, Root=%x\n", newAccountsState.TxId, newAccountsState.TxHash)
	printDashes()
}

func (h *CommandHandler) handlePropagateDID(parts []string) {
	if len(parts) < 3 || len(parts) > 4 {
		fmt.Println("Usage: propagateDID <did> <public_key> [balance]")
		return
	}
	did := parts[1]
	publicKey := parts[2]

	// Default balance is "0" if not provided
	balance := "0"
	if len(parts) == 4 {
		balance = parts[3]
	}

	// First create document then propagate
	err := DB_OPs.CreateAccount(nil, did, common.HexToAddress(publicKey), nil)
	if err != nil {
		fmt.Printf("Failed to create account: %v\n", err)
		return
	}

	fmt.Printf("Propagating DID %s with public key %s and balance %s to the network...\n",
		did, publicKey, balance)

	Document, err := DB_OPs.GetAccount(nil, common.HexToAddress(publicKey))
	if err != nil {
		fmt.Printf("Failed to get account: %v\n", err)
		return
	}

	// First create the DID and then propagate
	err = messaging.PropagateDID(h.Node.Host, Document)
	if err != nil {
		fmt.Printf("Failed to propagate DID: %v\n", err)
	} else {
		fmt.Println("DID propagated successfully to all connected peers")
	}
}

func (h *CommandHandler) handleSyncInfo() {
	fmt.Println("FastSync Configuration:")
	fmt.Printf("  Batch Size: %d\n", fastsync.SyncBatchSize)
	fmt.Printf("  Request Timeout: %v\n", fastsync.RequestTimeout)
	fmt.Printf("  Response Timeout: %v\n", fastsync.ResponseTimeout)
	printDashes()
}

func (h *CommandHandler) handleGetDID(parts []string) {
	if len(parts) != 2 {
		fmt.Println("Usage: getDID <did>")
		return
	}
	did := parts[1]

	// It can take DID or Address
	// if prefix is DID: then its a DID else Account
	var doc *DB_OPs.Account
	var err error
	if strings.HasPrefix(did, DB_OPs.DIDPrefix) {
		doc, err = DB_OPs.GetAccountByDID(h.DIDClient, did)
		if err != nil {
			fmt.Printf("Failed to retrieve DID %s: %v\n", did, err)
			return
		}
	} else {
		addr := common.HexToAddress(did)
		doc, err = DB_OPs.GetAccount(h.DIDClient, addr)
		if err != nil {
			fmt.Printf("Failed to retrieve Address %s: %v\n", did, err)
			return
		}
	}

	fmt.Println("DID Document:")
	fmt.Printf("  DID: %s\n", doc.DIDAddress)
	fmt.Printf("  Public Key: %s\n", doc.Address)
	fmt.Printf("  Account Type: %s\n", doc.AccountType)
	fmt.Printf("  Nonce: %d\n", doc.Nonce)
	fmt.Printf("  Balance: %s\n", doc.Balance)
	fmt.Printf("  Created: %s\n", time.Unix(doc.CreatedAt, 0).Format(time.RFC3339))
	fmt.Printf("  Updated: %s\n", time.Unix(doc.UpdatedAt, 0).Format(time.RFC3339))
	fmt.Printf("  Metadata: %s\n", doc.Metadata)
}

func (h *CommandHandler) handleDBState() {

	err := h.checkDBClient()
	if err != nil {
		fmt.Printf("Database client not initialized: %v\n", err)
		return
	}

	err = h.checkDIDClient()
	if err != nil {
		fmt.Printf("DID database client not initialized: %v\n", err)
		return
	}

	// Debugging
	fmt.Println("Got DB Client and DID Client", h.MainClient.Client, h.DIDClient.Client)

	state, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		fmt.Printf("Failed to get database state: %v\n", err)
		return
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
		keys, err := DB_OPs.GetAllKeys(h.MainClient, lastKey)
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
}

// handleListAliases shows the alias of the current node
func (h *CommandHandler) handleListAliases() {
	if h.SeedNode == "" {
		fmt.Println("❌ No seed node configured. Cannot check alias.")
		return
	}

	fmt.Printf("📋 Checking alias for current node from seed node: %s\n", h.SeedNode)

	// Create seed node client
	client, err := seednode.NewClient(h.SeedNode)
	if err != nil {
		fmt.Printf("❌ Failed to connect to seed node: %v\n", err)
		return
	}
	defer client.Close()

	// Get current node's peer ID
	currentPeerID := h.Node.Host.ID().String()
	fmt.Printf("🔍 Current node peer ID: %s\n", currentPeerID)

	// Try to get the current peer's record
	peerRecord, err := client.GetPeer(currentPeerID)
	if err != nil {
		fmt.Printf("❌ Current node not found in seed node: %v\n", err)
		fmt.Println("💡 Make sure this node is registered with the seed node.")
		return
	}

	fmt.Printf("✅ Current node found in seed node (seq: %d)\n", peerRecord.Seq)

	// Try to get the alias for this peer
	alias, err := client.GetAliasByPeerID(currentPeerID)
	if err != nil {
		fmt.Printf("❌ No alias found for this peer: %v\n", err)
		fmt.Println("💡 This node is registered but doesn't have an alias.")
	} else {
		fmt.Printf("✅ Alias found: %s\n", alias)
	}

	fmt.Println("\n📊 Current Node Information:")
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("Peer ID: %s\n", peerRecord.PeerId)
	fmt.Printf("Sequence: %d\n", peerRecord.Seq)
	fmt.Printf("Status: %s\n", peerRecord.CurrentStatus.String())
	fmt.Printf("Multiaddrs: %d addresses\n", len(peerRecord.Multiaddrs))

	if len(peerRecord.Multiaddrs) > 0 {
		fmt.Println("Addresses:")
		for i, addr := range peerRecord.Multiaddrs {
			if i < 3 { // Show first 3 addresses
				fmt.Printf("  %d. %s\n", i+1, addr)
			}
		}
		if len(peerRecord.Multiaddrs) > 3 {
			fmt.Printf("  ... and %d more addresses\n", len(peerRecord.Multiaddrs)-3)
		}
	}

	printDashes()
}

func (h *CommandHandler) handleDiscoverNeighbors() {
	if h.SeedNode == "" {
		fmt.Println("❌ No seed node specified. Use -seednode flag to specify a seed node.")
		return
	}

	fmt.Printf("🔍 Starting neighbor discovery from seed node: %s\n", h.SeedNode)
	printDashes()

	// Create seed node client
	client, err := seednode.NewClient(h.SeedNode)
	if err != nil {
		fmt.Printf("❌ Failed to connect to seed node: %v\n", err)
		return
	}
	defer client.Close()

	// Perform neighbor discovery
	err = client.DiscoverAndAddNeighbors(h.Node.Host, h.NodeManager)
	if err != nil {
		fmt.Printf("❌ Neighbor discovery failed: %v\n", err)
	} else {
		fmt.Println("✅ Neighbor discovery completed successfully")
	}
	printDashes()
}

func (h *CommandHandler) checkDBClient() error {
	if h.MainClient == nil {
		return fmt.Errorf("database client not initialized")
	}
	return nil
}

// LatencyStats holds latency measurement results
type LatencyStats struct {
	Average    float64
	Minimum    float64
	Maximum    float64
	Successful int
	Total      int
}

// measureLatency performs multiple ping tests to measure network latency
func (h *CommandHandler) measureLatency(client *seednode.Client) LatencyStats {
	const numPings = 5
	var latencies []float64
	successful := 0

	fmt.Printf("  Performing %d ping tests...\n", numPings)

	for i := 0; i < numPings; i++ {
		start := time.Now()

		// Use a short timeout for each ping
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.HealthCheck(ctx)
		cancel()

		latency := float64(time.Since(start).Nanoseconds()) / 1e6 // Convert to milliseconds

		if err != nil {
			fmt.Printf("    Ping %d: ❌ Failed (%.2f ms) - %v\n", i+1, latency, err)
		} else {
			fmt.Printf("    Ping %d: ✅ %.2f ms\n", i+1, latency)
			latencies = append(latencies, latency)
			successful++
		}

		// Small delay between pings
		if i < numPings-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	stats := LatencyStats{
		Total:      numPings,
		Successful: successful,
	}

	if len(latencies) > 0 {
		// Calculate statistics
		var sum float64
		stats.Minimum = latencies[0]
		stats.Maximum = latencies[0]

		for _, latency := range latencies {
			sum += latency
			if latency < stats.Minimum {
				stats.Minimum = latency
			}
			if latency > stats.Maximum {
				stats.Maximum = latency
			}
		}

		stats.Average = sum / float64(len(latencies))
	}

	return stats
}

// checkDIDClient ensures the DID database client is properly initialized before use
func (h *CommandHandler) checkDIDClient() error {
	if h.DIDClient == nil {
		return fmt.Errorf("DID database client not initialized")
	}
	return nil
}

// handleGethStatus displays the current gETH configuration
func (h *CommandHandler) handleGethStatus() {
	fmt.Println(config.ColorGreen + "=== gETH Status ===" + config.ColorReset)
	fmt.Printf("Chain ID: %d\n", h.ChainID)
	fmt.Printf("Facade Port: %d\n", h.FacadePort)
	fmt.Printf("WebSocket Port: %d\n", h.WSPort)

	// Show status of services
	if h.FacadePort > 0 {
		fmt.Printf("Facade Server: %sRunning%s (http://localhost:%d)\n",
			config.ColorGreen, config.ColorReset, h.FacadePort)
	} else {
		fmt.Printf("Facade Server: %sDisabled%s\n",
			config.ColorYellow, config.ColorReset)
	}

	if h.WSPort > 0 {
		fmt.Printf("WebSocket Server: %sRunning%s (ws://localhost:%d)\n",
			config.ColorGreen, config.ColorReset, h.WSPort)
	} else {
		fmt.Printf("WebSocket Server: %sDisabled%s\n",
			config.ColorYellow, config.ColorReset)
	}
}
