package messaging

import (
    "bufio"
    "context"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "strconv"
    "sync"
    "time"

    "github.com/bits-and-blooms/bloom/v3"
    "github.com/libp2p/go-libp2p/core/host"
    "github.com/libp2p/go-libp2p/core/network"
    "github.com/libp2p/go-libp2p/core/peer"
    "github.com/rs/zerolog/log"

    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/helper"
    "gossipnode/messaging/BlockProcessing"
    "gossipnode/metrics"
)

// Global variables for block propagation
var (
    peerTimeouts     = make(map[string]time.Time)
    peerTimeoutMutex sync.RWMutex
    messageFilter    *bloom.BloomFilter
    immuClient       *config.ImmuClient
    immuClientOnce   sync.Once
    globalHost       host.Host // Add this line
)

func init() {
    messageFilter = bloom.NewWithEstimates(10000, 0.01)
    go cleanupPeerTimeouts()
}

// Initialize the host when starting the node
func InitBlockPropagation(h host.Host) error {
    globalHost = h  // Save the host reference
    
    var initErr error
    immuClientOnce.Do(func() {
        // Initialize ImmuDB client for block propagation system
        client, err := DB_OPs.New()
        if err != nil {
            initErr = fmt.Errorf("failed to initialize ImmuDB client: %w", err)
            return
        }
        immuClient = client
        log.Info().Msg("Block propagation system initialized")
    })
    return initErr
}


// generateBlockMessageID creates a unique ID for a block message
func generateBlockMessageID(sender, nonce string, timestamp int64) string {
    hasher := sha256.New()
    hasher.Write([]byte(fmt.Sprintf("%s-%s-%d", sender, nonce, timestamp)))
    hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
    return hash[:16] // Return first 16 chars for brevity
}

// cleanupPeerTimeouts periodically removes expired peer timeouts
func cleanupPeerTimeouts() {
    for {
        time.Sleep(10 * time.Second)
        peerTimeoutMutex.Lock()
        for peerID, until := range peerTimeouts {
            if time.Now().After(until) {
                delete(peerTimeouts, peerID)
            }
        }
        peerTimeoutMutex.Unlock()
    }
}

// isPeerTimedOut checks if a peer is currently timed out
func isPeerTimedOut(peerID string) bool {
    peerTimeoutMutex.RLock()
    defer peerTimeoutMutex.RUnlock()
    timeout, exists := peerTimeouts[peerID]
    if !exists {
        return false
    }
    return time.Now().Before(timeout)
}

// timeoutPeer sets a timeout for a specific peer
func timeoutPeer(peerID string, duration time.Duration) {
    peerTimeoutMutex.Lock()
    defer peerTimeoutMutex.Unlock()
    
    peerTimeouts[peerID] = time.Now().Add(duration)
    log.Info().
        Str("peer", peerID).
        Dur("duration", duration).
        Msg("Peer timed out for sending duplicate block")
}

// isMessageProcessed checks if this message has already been processed
func isMessageProcessed(messageID string) bool {
    return messageFilter.Test([]byte(messageID))
}

// markMessageProcessed marks a message as processed
func markMessageProcessed(messageID string) {
    messageFilter.Add([]byte(messageID))
}

// storeMessageInImmuDB stores a message in ImmuDB using the appropriate key
func storeMessageInImmuDB(msg config.BlockMessage) error {
    // Determine the key - focus on ZK blocks
    var key string
    if msg.Type == "zkblock" && msg.ZKBlock != nil {
        key = fmt.Sprintf("zkblock:%s", msg.ZKBlock.BlockHash.Hex())
    } else if msg.Type == "transaction" && msg.Data != nil && msg.Data["transaction_hash"] != "" {
        key = fmt.Sprintf("tx:%s", msg.Data["transaction_hash"])
    } else {
        key = fmt.Sprintf("crdt:nonce:%s", msg.Nonce)
    }

    // Store the message
    if err := DB_OPs.Create(immuClient, key, msg); err != nil {
        log.Error().Err(err).Str("key", key).Msg("Failed to store message in ImmuDB")
        return err
    }
    
    // Update message set
    if err := updateMessageSet(key); err != nil {
        log.Error().Err(err).Str("key", key).Msg("Failed to update message set")
        return err
    }
    
    log.Debug().Str("key", key).Str("type", msg.Type).Msg("Message stored in ImmuDB")
    return nil
}

// updateMessageSet adds a message key to the grow-only set in ImmuDB
func updateMessageSet(key string) error {
    const setKey = "crdt:message_set"
    
    var messageSet map[string]bool
    err := DB_OPs.ReadJSON(immuClient, setKey, &messageSet)
    if err != nil {
        messageSet = make(map[string]bool)
    }
    
    messageSet[key] = true
    return DB_OPs.Create(immuClient, setKey, messageSet)
}

// getMessageIDForBloomFilter gets the appropriate ID to use for duplication checking
func getMessageIDForBloomFilter(msg config.BlockMessage) string {
    // Special handling for ZK blocks to use hash for deduplication
    if msg.Type == "zkblock" && msg.ZKBlock != nil {
        return fmt.Sprintf("zkblock:%s", msg.ZKBlock.BlockHash.Hex())
    }
    
    if msg.Type == "transaction" && msg.Data != nil && msg.Data["transaction_hash"] != "" {
        return msg.Data["transaction_hash"]
    }
    
    return msg.Nonce
}

// HandleBlockStream processes incoming block propagation messages
// HandleBlockStream processes incoming block propagation messages
// Priority: FORWARD FIRST, then PROCESS/VALIDATE before STORING
func HandleBlockStream(stream network.Stream) {
    defer stream.Close()
    
    remotePeer := stream.Conn().RemotePeer().String()
    if isPeerTimedOut(remotePeer) {
        log.Debug().Str("peer", remotePeer).Msg("Ignoring message from timed-out peer")
        return
    }

    metrics.MessagesReceivedCounter.WithLabelValues("block", remotePeer).Inc()
    
    // Read the message
    reader := bufio.NewReader(stream)
    messageBytes, err := reader.ReadBytes('\n')
    if err != nil && err != io.EOF {
        log.Error().Err(err).Msg("Failed to read message bytes")
        return
    }

    // Parse the message
    var msg config.BlockMessage
    if err := json.Unmarshal(messageBytes, &msg); err != nil {
        log.Error().Err(err).Msg("Failed to unmarshal block message")
        return
    }

    // Check for duplicates
    messageID := getMessageIDForBloomFilter(msg)
    if isMessageProcessed(messageID) {
        log.Debug().Str("message_id", messageID).Msg("Duplicate message received")
        timeoutPeer(remotePeer, 20*time.Second)
        return
    }
    
    // Mark as processed to prevent duplicate processing
    markMessageProcessed(messageID)

    // For ZK blocks, prioritize forwarding over processing
    if msg.Type == "zkblock" && msg.ZKBlock != nil {
        log.Info().
            Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
            Uint64("block_number", msg.ZKBlock.BlockNumber).
            Int("txn_count", len(msg.ZKBlock.Transactions)).
            Msg("Received ZK block from peer")

        // STEP 1: FORWARD BLOCK FIRST - increment hops and forward to other peers
        if msg.Hops < config.MaxHops {
            msg.Hops++
            if globalHost != nil {
                log.Info().
                    Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                    Uint64("block_number", msg.ZKBlock.BlockNumber).
                    Int("hops", msg.Hops).
                    Msg("Forwarding ZK block to peers")
                    
                // Don't wait for forwarding to complete
                go forwardBlock(globalHost, msg)
            } else {
                log.Error().Msg("Cannot forward block: global host not initialized")
            }
        }

        // STEP 2: PROCESS AND VALIDATE BLOCK AFTERWARD
        go func() {
            // Create DB clients for processing
            mainDBClient, err := DB_OPs.New()
            if err != nil {
                log.Error().Err(err).Msg("Failed to create main DB client")
                return
            }
            defer DB_OPs.Close(mainDBClient)
            
            accountsClient, err := DB_OPs.New(DB_OPs.WithDatabase(config.AccountsDBName))
            if err != nil {
                log.Error().Err(err).Msg("Failed to create accounts DB client")
                return
            }
            defer DB_OPs.Close(accountsClient)
            
            log.Info().
                Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                Uint64("block_number", msg.ZKBlock.BlockNumber).
                Msg("Processing block transactions")
                
            // Process all transactions in the block atomically with rollback capability
            if err := BlockProcessing.ProcessBlockTransactions(msg.ZKBlock, accountsClient); err != nil {
                log.Error().
                    Err(err).
                    Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                    Msg("Block processing failed - not storing block")
                return
            }
            
            log.Info().
                Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                Msg("All transactions processed successfully - storing block")
                
            // Store the validated and processed block in main DB
            if err := DB_OPs.StoreZKBlock(mainDBClient, msg.ZKBlock); err != nil {
                log.Error().
                    Err(err).
                    Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                    Msg("Failed to store block in database")
                return
            }
            
            // Store block message metadata
            if err := storeMessageInImmuDB(msg); err != nil {
                log.Error().Err(err).Msg("Failed to store block message in ImmuDB")
            }
            
            log.Info().
                Str("block_hash", msg.ZKBlock.BlockHash.Hex()).
                Uint64("block_number", msg.ZKBlock.BlockNumber).
                Msg("Block processed and stored successfully")
        }()
        
        // Print to console
        fmt.Printf("\n[ZKBLOCK from %s] Block #%d, Hash: %s, Txns: %d\n>>> ", 
            msg.Sender, msg.ZKBlock.BlockNumber, msg.ZKBlock.BlockHash.Hex(), 
            len(msg.ZKBlock.Transactions))
    } else {
        // Handle other message types (not our focus)
        if msg.Hops < config.MaxHops {
            msg.Hops++
            go forwardBlock(globalHost, msg)
        }
    }
    
    // Notify explorer or other UI components
    helper.NotifyBroadcast(msg)
}

// forwardBlock sends the block message to all connected peers
func forwardBlock(h host.Host, msg config.BlockMessage) {
    peers := h.Network().Peers()
    
    // Convert message to JSON
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        log.Error().Err(err).Msg("Failed to marshal block message")
        return
    }
    msgBytes = append(msgBytes, '\n')
    
    // Track forwarding metrics
    var successCount int
    var successMutex sync.Mutex
    var wg sync.WaitGroup
    
    // Send to each peer concurrently
    for _, peerID := range peers {
        // Don't send back to the original sender
        if peerID.String() == msg.Sender {
            continue
        }
        
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
            if err != nil {
                log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
                return
            }
            defer stream.Close()
            
            if _, err := stream.Write(msgBytes); err != nil {
                log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to write message")
                return
            }
            
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            metrics.MessagesSentCounter.WithLabelValues(msg.Type, peer.String()).Inc()
        }(peerID)
    }
    
    wg.Wait()
    
    log.Info().
        Str("type", msg.Type).
        Int("success", successCount).
        Int("total", len(peers) - 1).
        Msg("Block forwarded to peers")
}

// PropagateZKBlock creates and propagates a complete ZK block to the network
// PropagateZKBlock creates and propagates a complete ZK block to the network
// This function is called by Server.go when receiving a new block via API
func PropagateZKBlock(h host.Host, block *config.ZKBlock) error {
    // Step 1: Set up database connections
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        return fmt.Errorf("failed to create main DB client: %w", err)
    }
    defer DB_OPs.Close(mainDBClient)
    
    accountsClient, err := DB_OPs.New(DB_OPs.WithDatabase(config.AccountsDBName))
    if err != nil {
        return fmt.Errorf("failed to create accounts DB client: %w", err)
    }
    defer DB_OPs.Close(accountsClient)
    
    log.Info().
        Str("block_hash", block.BlockHash.Hex()).
        Uint64("block_number", block.BlockNumber).
        Int("txn_count", len(block.Transactions)).
        Msg("Starting ZK block propagation")
    
    // Step 2: Process the entire block atomically with proper rollback capability
    if err := BlockProcessing.ProcessBlockTransactions(block, accountsClient); err != nil {
        return fmt.Errorf("block validation failed: %w", err)
    }
    
    // Step 3: Store the block in main DB - all transactions are valid
    if err := DB_OPs.StoreZKBlock(mainDBClient, block); err != nil {
        return fmt.Errorf("failed to store block: %w", err)
    }
    
    // Step 4: Generate a unique nonce for the block message
    nonceBytes := make([]byte, 16)
    for i := range nonceBytes {
        nonceBytes[i] = byte(time.Now().UnixNano() & 0xff)
        time.Sleep(1 * time.Nanosecond)
    }
    nonce := base64.URLEncoding.EncodeToString(nonceBytes)
    
    // Step 5: Create summary metadata for logs and Bloom filter
    metadata := map[string]string{
        "block_hash":    block.BlockHash.Hex(),
        "block_number":  strconv.FormatUint(block.BlockNumber, 10),
        "txn_count":     strconv.Itoa(len(block.Transactions)),
        "proof_hash":    block.ProofHash,
        "status":        block.Status,
        "timestamp":     strconv.FormatInt(block.Timestamp, 10),
    }
    
    // Add sample transaction hashes
    txLimit := min(5, len(block.Transactions))
    for i := 0; i < txLimit; i++ {
        metadata[fmt.Sprintf("tx_%d", i)] = block.Transactions[i].Hash
    }
    
    // Step 6: Create block message with full ZK block data
    now := time.Now().Unix()
    msg := config.BlockMessage{
        Sender:    h.ID().String(),
        Timestamp: now,
        Nonce:     nonce,
        Data:      metadata,
        ZKBlock:   block,      // Include full block
        Type:      "zkblock",
        Hops:      0,
    }
    
    // Generate message ID
    msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
    
    // Mark as processed by us to avoid processing our own message
    markMessageProcessed(getMessageIDForBloomFilter(msg))
    
    // Store block message metadata
    if err := storeMessageInImmuDB(msg); err != nil {
        log.Error().Err(err).Msg("Failed to store block message in ImmuDB")
        // Continue even if metadata storage fails
    }
    
    // Step 7: Propagate to peers
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("failed to marshal block message: %w", err)
    }
    msgBytes = append(msgBytes, '\n')
    
    // Get connected peers
    peers := h.Network().Peers()
    if len(peers) == 0 {
        log.Warn().Msg("No connected peers to propagate ZK block to")
        return nil // Not an error, just no peers to propagate to
    }
    
    // Send to all peers concurrently
    var wg sync.WaitGroup
    var successCount int
    var successMutex sync.Mutex
    
    for _, peerID := range peers {
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            // Use the correct protocol ID from constants
            stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
            if err != nil {
                log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
                return
            }
            defer stream.Close()
            
            if _, err := stream.Write(msgBytes); err != nil {
                log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to write message")
                return
            }
            
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            metrics.MessagesSentCounter.WithLabelValues("zkblock", peer.String()).Inc()
        }(peerID)
    }
    
    wg.Wait()
    
    log.Info().
        Str("block_hash", block.BlockHash.Hex()).
        Int("success", successCount).
        Int("total", len(peers)).
        Msg("ZK block propagated successfully")
    
    return nil
}

// GetAllMessages retrieves all message keys from ImmuDB
func GetAllMessages() ([]string, error) {
    const setKey = "crdt:message_set"
    
    var messageSet map[string]bool
    if err := DB_OPs.ReadJSON(immuClient, setKey, &messageSet); err != nil {
        return nil, fmt.Errorf("failed to read message set: %w", err)
    }
    
    keys := make([]string, 0, len(messageSet))
    for key := range messageSet {
        keys = append(keys, key)
    }
    
    return keys, nil
}

// GetMessage retrieves a message by key
func GetMessage(key string) (*config.BlockMessage, error) {
    var message config.BlockMessage
    if err := DB_OPs.ReadJSON(immuClient, key, &message); err != nil {
        return nil, fmt.Errorf("failed to read message data: %w", err)
    }
    
    return &message, nil
}

// GetZKBlockByHash retrieves a ZK block by its hash
func GetZKBlockByHash(blockHash string) (*config.ZKBlock, error) {
    key := fmt.Sprintf("zkblock:%s", blockHash)
    
    msg, err := GetMessage(key)
    if err != nil {
        return nil, fmt.Errorf("block not found: %w", err)
    }
    
    if msg.ZKBlock == nil {
        return nil, fmt.Errorf("message found but does not contain a ZK block")
    }
    
    return msg.ZKBlock, nil
}

// Helper function for min value
func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}