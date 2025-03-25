package messaging

import (
    "bufio"
    "context"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "sync"
    "time"

    "github.com/bits-and-blooms/bloom/v3"
    "github.com/libp2p/go-libp2p/core/host"
    "github.com/libp2p/go-libp2p/core/network"
    "github.com/libp2p/go-libp2p/core/peer"
    "github.com/rs/zerolog/log"

    // "gossipnode/"
    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/helper"
    "gossipnode/metrics"
)

// BlockMessage represents a message for block propagation
// Enhanced to support both traditional map data and structured transaction objects
// Store for peer timeouts
var (
    peerTimeouts     = make(map[string]time.Time)
    peerTimeoutMutex sync.RWMutex
)

// Bloom filter for efficient message ID checking
var messageFilter *bloom.BloomFilter

// ImmuDB client instance
var (
    immuClient     *config.ImmuClient
    immuClientOnce sync.Once
)

func init() {
    // Initialize a bloom filter with 10000 items and 0.01 false positive rate
    messageFilter = bloom.NewWithEstimates(10000, 0.01)
    
    // Start the cleanup routine for peer timeouts
    go cleanupPeerTimeouts()
}

// InitBlockPropagation initializes the block propagation system
func InitBlockPropagation() error {
    var initErr error
    
    immuClientOnce.Do(func() {
        // Create ImmuDB client
        client, err := DB_OPs.New()
        if err != nil {
            initErr = fmt.Errorf("failed to create ImmuDB client: %w", err)
            return
        }
        immuClient = client
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
        time.Sleep(30 * time.Second)
        
        peerTimeoutMutex.Lock()
        now := time.Now()
        for peer, timeout := range peerTimeouts {
            if now.After(timeout) {
                delete(peerTimeouts, peer)
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
    log.Info().Str("peer", peerID).Dur("duration", duration).Msg("Peer timed out for sending duplicate block")
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
func storeMessageInImmuDB(msg config.BlockMessage) {
    // Store in ImmuDB in a separate goroutine to prevent blocking
    go func() {
        var key string
        
        // For transactions, use the transaction hash as the key
        if msg.Type == "transaction" && msg.Data != nil {
            if txHash, ok := msg.Data["transaction_hash"]; ok && txHash != "" {
                key = fmt.Sprintf("tx:%s", txHash)
                log.Debug().Str("tx_hash", txHash).Msg("Using transaction hash as key")
            } else if txHash, ok := msg.Data["transaction_id"]; ok && txHash != "" {
                key = fmt.Sprintf("tx:%s", txHash)
                log.Debug().Str("tx_id", txHash).Msg("Using transaction ID as key")
            } else {
                // Fallback to nonce if no transaction hash
                key = fmt.Sprintf("crdt:nonce:%s", msg.Nonce)
                log.Debug().Str("nonce", msg.Nonce).Msg("Using nonce as key (no transaction hash found)")
            }
        } else {
            // For other message types, use the nonce
            key = fmt.Sprintf("crdt:nonce:%s", msg.Nonce)
        }
        
        // Store the update
        err := DB_OPs.Create(immuClient, key, msg)
        if err != nil {
            log.Error().Err(err).Str("key", key).Msg("Failed to store message in ImmuDB")
            return
        }
        
        // Also update the message set
        err = updateMessageSet(key)
        if err != nil {
            log.Error().Err(err).Str("key", key).Msg("Failed to update message set")
        }
        
        log.Info().Str("key", key).Str("type", msg.Type).Msg("Successfully stored message in ImmuDB")
    }()
}

// updateMessageSet adds a message key to the grow-only set in ImmuDB
func updateMessageSet(key string) error {
    const setKey = "crdt:message_set"
    
    // Try to get the current set
    var messageSet map[string]bool
    err := DB_OPs.ReadJSON(immuClient, setKey, &messageSet)
    
    // If not found or error, start with empty set
    if err != nil {
        messageSet = make(map[string]bool)
    }
    
    // Add the new key (idempotent operation)
    messageSet[key] = true
    
    // Store the updated set
    return DB_OPs.Create(immuClient, setKey, messageSet)
}

// getMessageIDForBloomFilter gets the appropriate ID to use for duplication checking
func getMessageIDForBloomFilter(msg config.BlockMessage) string {
    // For transactions, use transaction hash if available
    if msg.Type == "transaction" && msg.Data != nil {
        if txHash, ok := msg.Data["transaction_hash"]; ok && txHash != "" {
            return txHash
        } else if txHash, ok := msg.Data["transaction_id"]; ok && txHash != "" {
            return txHash
        }
    }
    
    // Otherwise use the nonce
    return msg.Nonce
}

// HandleBlockStream processes incoming block propagation messages
func HandleBlockStream(stream network.Stream) {
    defer stream.Close()
    
    // Get the remote peer
    remotePeer := stream.Conn().RemotePeer().String()
    
    // Check if peer is timed out
    if isPeerTimedOut(remotePeer) {
        log.Debug().Str("peer", remotePeer).Msg("Ignoring message from timed out peer")
        return
    }
    
    // Record metrics
    metrics.MessagesReceivedCounter.WithLabelValues("block", remotePeer).Inc()
    
    // Read the incoming message
    reader := bufio.NewReader(stream)
    messageBytes, err := reader.ReadBytes('\n')
    if err != nil {
        if err != io.EOF {
            log.Error().Err(err).Str("peer", remotePeer).
                Msg("Error reading message")
        }
        return
    }
    
    // Parse the message
    var msg config.BlockMessage
    if err := json.Unmarshal(messageBytes, &msg); err != nil {
        log.Error().Err(err).Msg("Failed to unmarshal message")
        return
    }
    
    // Get the appropriate ID for duplication checking
    messageID := getMessageIDForBloomFilter(msg)
    
    // Check if we've already processed this message
    if isMessageProcessed(messageID) {
        log.Debug().Str("message_id", messageID).Msg("Duplicate message received, timing out peer")
        timeoutPeer(remotePeer, 20*time.Second)
        return
    }
    
    // Mark message as processed
    markMessageProcessed(messageID)
    
    // Process the message - update our CRDT state in ImmuDB
    storeMessageInImmuDB(msg)
    
    // Log receipt based on message type
    if msg.Type == "transaction" {
        var txHash string
        if msg.Data != nil {
            txHash = msg.Data["transaction_hash"]
            if txHash == "" {
                txHash = msg.Data["transaction_id"]
            }
        }
        fmt.Printf("\n[TRANSACTION from %s] Hash: %s\n>>> ", msg.Sender, txHash)
    } else {
        fmt.Printf("\n[BLOCK from %s] Nonce: %s\n>>> ", msg.Sender, msg.Nonce)
    }
    
    // Notify explorer
    helper.NotifyBroadcast(msg)
    
    // Only rebroadcast if we haven't reached max hops
    if msg.Hops < config.MaxHops {
        // Forward to our peers
        msg.Hops++
        localPeer := stream.Conn().LocalPeer().String()
        log.Info().
            Str("msg_id", msg.ID).
            Str("type", msg.Type).
            Str("origin", msg.Sender).
            Str("via", localPeer).
            Int("hops", msg.Hops).
            Msg("Propagating message")
        
        // Forward the message to other peers
        if hostInstance := getHostInstance(); hostInstance != nil {
            go forwardBlock(hostInstance, msg)
        } else {
            log.Error().Msg("Cannot access host instance for forwarding message")
        }
    } else {
        log.Info().
            Str("msg_id", msg.ID).
            Str("type", msg.Type).
            Int("hops", msg.Hops).
            Msg("Max hops reached, not propagating message")
    }
}

// forwardBlock sends the block message to all connected peers
func forwardBlock(h host.Host, msg config.BlockMessage) {
    // Get all connected peers
    peers := h.Network().Peers()
    
    // Convert message to JSON
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        log.Error().Err(err).Msg("Failed to marshal message")
        return
    }
    msgBytes = append(msgBytes, '\n')
    
    // Track how many peers we successfully broadcasted to
    var successCount int
    var successMutex sync.Mutex
    var wg sync.WaitGroup
    
    // Send to each peer (except original sender) concurrently
    for _, peerID := range peers {
        // Don't send back to the original sender
        if peerID.String() == msg.Sender {
            continue
        }
        
        // Don't send to timed out peers
        if isPeerTimedOut(peerID.String()) {
            continue
        }
        
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            // Open a stream to the peer
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
                return
            }
            defer stream.Close()
            
            // Write the message
            _, err = stream.Write(msgBytes)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to write message")
                return
            }
            
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            // Record metrics
            metrics.MessagesSentCounter.WithLabelValues(msg.Type, peer.String()).Inc()
        }(peerID)
    }
    
    // Wait for all sends to complete
    wg.Wait()
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("type", msg.Type).
        Int("peers", successCount).
        Msg("Message propagated to peers")
}

// PropagateBlock creates and propagates a new generic block to the network
func PropagateBlock(h host.Host, data map[string]string) error {
    // Generate a unique nonce
    nonceBytes := make([]byte, 16)
    for i := range nonceBytes {
        nonceBytes[i] = byte(time.Now().UnixNano() & 0xff)
        time.Sleep(1 * time.Nanosecond)
    }
    nonce := base64.URLEncoding.EncodeToString(nonceBytes)
    
    // Create a new block message
    now := time.Now().Unix()
    msg := config.BlockMessage{
        Sender:    h.ID().String(),
        Timestamp: now,
        Nonce:     nonce,
        Data:      data,
        Type:      "block",
        Hops:      0,
    }
    
    // Generate a unique ID based on nonce and timestamp
    msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
    
    return propagateMessage(h, msg)
}

// PropagateTransaction creates and propagates a new transaction to the network
func PropagateTransaction(h host.Host, tx *config.Transaction, txHash string) error {
    // Generate a unique nonce
    nonceBytes := make([]byte, 16)
    for i := range nonceBytes {
        nonceBytes[i] = byte(time.Now().UnixNano() & 0xff)
        time.Sleep(1 * time.Nanosecond)
    }
    nonce := base64.URLEncoding.EncodeToString(nonceBytes)
    
    // Create transaction metadata as map for compatibility
    data := map[string]string{
        "transaction_hash": txHash,
    }
    
    // Create a new message
    now := time.Now().Unix()
    msg := config.BlockMessage{
        Sender:      h.ID().String(),
        Timestamp:   now,
        Nonce:       nonce,
        Data:        data,
        Transaction: tx,
        Type:        "transaction",
        Hops:        0,
    }
    
    // Generate a unique ID based on nonce and timestamp
    msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
    
    return propagateMessage(h, msg)
}

// propagateMessage is a shared implementation for propagating any message type
func propagateMessage(h host.Host, msg config.BlockMessage) error {
    // Get the appropriate ID for duplication checking
    messageID := getMessageIDForBloomFilter(msg)
    
    // First, update our own CRDT state
    storeMessageInImmuDB(msg)
    
    // Mark this message as processed by us
    markMessageProcessed(messageID)
    
    // Convert to JSON
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("failed to marshal message: %w", err)
    }
    msgBytes = append(msgBytes, '\n')
    
    // Get all connected peers
    peers := h.Network().Peers()
    if len(peers) == 0 {
        return fmt.Errorf("no connected peers to propagate to")
    }
    
    // Log based on message type
    if msg.Type == "transaction" {
        var txHash string
        if msg.Data != nil {
            txHash = msg.Data["transaction_hash"]
            if txHash == "" {
                txHash = msg.Data["transaction_id"]
            }
        }
        log.Info().
            Str("msg_id", msg.ID).
            Str("tx_hash", txHash).
            Str("type", msg.Type).
            Int("peers", len(peers)).
            Msg("Starting propagation to peers")
    } else {
        log.Info().
            Str("msg_id", msg.ID).
            Str("nonce", msg.Nonce).
            Str("type", msg.Type).
            Int("peers", len(peers)).
            Msg("Starting propagation to peers")
    }
    
    // Send message to all peers
    var wg sync.WaitGroup
    var successCount int
    var successMutex sync.Mutex
    
    for _, peerID := range peers {
        // Skip timed out peers
        if isPeerTimedOut(peerID.String()) {
            continue
        }
        
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            // Open stream to peer with timeout
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
                return
            }
            defer stream.Close()
            
            // Send the message
            _, err = stream.Write(msgBytes)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to send message")
                return
            }
            
            // Record success
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            // Record metrics
            metrics.MessagesSentCounter.WithLabelValues(msg.Type, peer.String()).Inc()
        }(peerID)
    }
    
    // Wait for all sends to complete
    wg.Wait()
    
    if successCount == 0 && len(peers) > 0 {
        return fmt.Errorf("failed to propagate to any peers")
    }
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("type", msg.Type).
        Int("success", successCount).
        Int("total", len(peers)).
        Msg("Propagation complete")
    
    helper.NotifyBroadcast(msg)
    return nil
}

// GetAllMessages retrieves all message keys from ImmuDB
func GetAllMessages() ([]string, error) {
    const setKey = "crdt:message_set"
    
    // Try to get the current set
    var messageSet map[string]bool
    err := DB_OPs.ReadJSON(immuClient,setKey, &messageSet)
    
    if err != nil {
        return nil, fmt.Errorf("failed to read message set: %w", err)
    }
    
    // Convert map keys to slice
    keys := make([]string, 0, len(messageSet))
    for key := range messageSet {
        keys = append(keys, key)
    }
    
    return keys, nil
}

// GetMessage retrieves a message by key
func GetMessage(key string) (*config.BlockMessage, error) {
    var message config.BlockMessage
    err := DB_OPs.ReadJSON(immuClient,key, &message)
    if err != nil {
        return nil, fmt.Errorf("failed to read message data: %w", err)
    }
    
    return &message, nil
}

// GetTransactionByHash retrieves a transaction by hash
func GetTransactionByHash(txHash string) (*config.BlockMessage, error) {
    key := fmt.Sprintf("tx:%s", txHash)
    return GetMessage(key)
}

// GetBlockByNonce retrieves a block by nonce
func GetBlockByNonce(nonce string) (*config.BlockMessage, error) {
    key := fmt.Sprintf("crdt:nonce:%s", nonce)
    return GetMessage(key)
}

// GetNonceData retrieves the data for a specific nonce (backward compatibility)
func GetNonceData(nonce string) (map[string]string, error) {
    message, err := GetBlockByNonce(nonce)
    if err != nil {
        return nil, err
    }
    
    // Return the data map
    return message.Data, nil
}