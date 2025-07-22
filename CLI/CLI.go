package CLI

import (
    "bufio"
    "fmt"
    "os"
    "strings"
    "sync"
    "time"

    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/fastsync"
    "gossipnode/messaging"
    "gossipnode/messaging/directMSG"
    "gossipnode/node"
    "gossipnode/seed"

    "github.com/libp2p/go-libp2p/core/peer"
    ma "github.com/multiformats/go-multiaddr"
)

// CommandHandler holds dependencies for CLI command execution
type CommandHandler struct {
    Node            *config.Node
    NodeManager     *node.NodeManager
    FastSyncer      *fastsync.FastSync
    MainClient      *config.ImmuClient
    DIDClient       *config.ImmuClient
    SeedNode        string
    EnableYggdrasil bool
}

// Simple helper to print the CLI prompt in color
func printPrompt() {
    fmt.Printf(config.ColorGreen + ">>> " + config.ColorReset)
}

func printDashes() {
    fmt.Println("\n", strings.Repeat("-", 50), "\n")
}

// StartCLI starts the interactive CLI
func (h *CommandHandler) StartCLI() error {
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
    fmt.Println("  syncinfo                          - Show FastSync configuration")
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

            h.handleCommand(parts)
            printPrompt()
        }
    }()

    wg.Wait()
    return nil
}

// handleCommand processes a single command
func (h *CommandHandler) handleCommand(parts []string) {
    switch parts[0] {
    case "msg":
        h.handleSendMessage(parts)
    case "ygg":
        h.handleYggdrasilMessage(parts)
    case "file":
        h.handleSendFile(parts)
    case "peers":
        h.handleRequestPeers(parts)
    case "addpeer":
        h.handleAddPeer(parts)
    case "removepeer":
        h.handleRemovePeer(parts)
    case "listpeers":
        h.handleListPeers()
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
    } else {
        fmt.Println("Message sent successfully")
    }
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
    if len(parts) != 3 {
        fmt.Println("Usage: file <peer_multiaddr> <filepath>")
        return
    }
    err := node.SendFile(h.Node, parts[1], parts[2])
    if err != nil {
        fmt.Println("Error:", err)
    } else {
        fmt.Println("File sent successfully")
    }
}

func (h *CommandHandler) handleRequestPeers(parts []string) {
    if h.SeedNode == "" {
        fmt.Println("No seed node specified. Use -connect flag to specify a seed node.")
        return
    }

    peers, err := seed.RequestPeers(h.Node.Host, h.SeedNode, 20, "")
    if err != nil {
        fmt.Printf("Error connecting to seed: %v\n", err)
    } else {
        fmt.Printf("Connected to seed. Discovered %d peers\n", len(peers))
        for i, p := range peers {
            fmt.Printf("  %d. ID: %s, Addresses: %v\n", i+1, p.ID, p.Addrs)
        }
    }
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
    mainState, err := DB_OPs.GetDatabaseState(h.MainClient)
    if err != nil {
        fmt.Printf("Failed to get main database state: %v\n", err)
        return
    }

    accountsState, err := DB_OPs.GetDatabaseState(h.DIDClient)
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

        syncErr = h.FastSyncer.StartSync(addrInfo.ID)
        if syncErr == nil {
            break
        }
    }

    if syncErr != nil {
        fmt.Printf("Sync failed after %d attempts: %v\n", maxRetries, syncErr)
        return
    }

    // Get post-sync states
    newMainState, err := DB_OPs.GetDatabaseState(h.MainClient)
    if err != nil {
        fmt.Printf("Failed to get main database state after sync: %v\n", err)
        return
    }

    newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient)
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

    value := DB_OPs.DIDDocument{
        DID:       did,
        PublicKey: publicKey,
        Balance:   balance,
    }
    fmt.Printf("Propagating DID %s with public key %s and balance %s to the network...\n",
        did, publicKey, balance)

    err := messaging.PropagateDID(h.Node.Host, &value)
    if err != nil {
        fmt.Printf("Failed to propagate DID: %v\n", err)
    } else {
        fmt.Println("DID propagated successfully to all connected peers")
    }
}

func (h *CommandHandler) handleSyncInfo() {
    fmt.Println("FastSync Configuration:")
    fmt.Printf("  Batch Size: %d\n", fastsync.SyncBatchSize)
    // fmt.Printf("  Bloom Filter Size: %d\n", fastsync.BloomFilterSize)
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

    doc, err := messaging.GetDID(did)
    if err != nil {
        fmt.Printf("Failed to retrieve DID %s: %v\n", did, err)
        return
    }

    fmt.Println("DID Document:")
    fmt.Printf("  DID: %s\n", doc.DID)
    fmt.Printf("  Public Key: %s\n", doc.PublicKey)
    fmt.Printf("  Balance: %s\n", doc.Balance)
    fmt.Printf("  Created: %s\n", time.Unix(doc.CreatedAt, 0).Format(time.RFC3339))
    fmt.Printf("  Updated: %s\n", time.Unix(doc.UpdatedAt, 0).Format(time.RFC3339))
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

    state, err := DB_OPs.GetDatabaseState(h.MainClient)
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

func (h *CommandHandler) checkDBClient() error {
    if h.MainClient == nil {
        return fmt.Errorf("database client not initialized")
    }
    return nil
}

// checkDIDClient ensures the DID database client is properly initialized before use
func (h *CommandHandler) checkDIDClient() error {
    if h.DIDClient == nil {
        return fmt.Errorf("DID database client not initialized")
    }
    return nil
}