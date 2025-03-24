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

    "gossipnode/Block"
    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/explorer"
    "gossipnode/metrics"
)

// BlockMessage represents a message for block propagation
// Enhanced to support both traditional map data and structured transaction objects
type BlockMessage struct {
    ID          string              `json:"id"`                    // Unique message ID
    Sender      string              `json:"sender"`                // Original sender's peer ID
    Timestamp   int64               `json:"timestamp"`             // Unix timestamp when message was created
    Nonce       string              `json:"nonce"`                 // Unique nonce for CRDT
    Data        map[string]string   `json:"data,omitempty"`        // Data payload for generic messages
    Transaction *Block.Transaction  `json:"transaction,omitempty"` // Structured transaction data
    Type        string              `json:"type"`                  // "transaction", "block", "message", etc.
    Hops        int                 `json:"hops"`                  // How many hops this message has made
}

// Store for peer timeouts
var (
    peerTimeouts     = make(map[string]time.Time)
    peerTimeoutMutex sync.RWMutex
)

// Bloom filter for efficient nonce checking
var nonceFilter *bloom.BloomFilter

// ImmuDB client instance
var (
    immuClient     *DB_OPs.ImmuClient
    immuClientOnce sync.Once
)

// Explorer reference
var explorerRef *explorer.Explorer

func SetExplorerRef(e *explorer.Explorer) {
    explorerRef = e
}

func notifyExplorer(msg BlockMessage) {
    // Skip if explorer reference isn't set
    if explorerRef == nil {
        log.Debug().Msg("Explorer reference not set")
        return
    }
    
    // Prepare notification message
    notification := map[string]interface{}{
        "type": msg.Type,
        "data": msg,
    }
    
    // Marshal to JSON
    data, err := json.Marshal(notification)
    if err != nil {
        log.Error().Err(err).Msg("Failed to marshal block notification")
        return
    }
    
    // Send to explorer for broadcasting
    e := explorerRef
    e.Broadcast <- data
    log.Debug().Str("block_id", msg.ID).Msg("Block notification sent to explorer")
}

func init() {
    // Initialize a bloom filter with 10000 items and 0.01 false positive rate
    nonceFilter = bloom.NewWithEstimates(10000, 0.01)
    
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

// isNonceProcessed checks if this nonce has already been processed
func isNonceProcessed(nonce string) bool {
    return nonceFilter.Test([]byte(nonce))
}

// markNonceProcessed marks a nonce as processed
func markNonceProcessed(nonce string) {
    nonceFilter.Add([]byte(nonce))
}

// storeNonceInImmuDB stores a message in ImmuDB
func storeNonceInImmuDB(nonce string, msg BlockMessage) {
    // Store in ImmuDB in a separate goroutine to prevent blocking
    go func() {
        // Create a key based on the nonce
        key := fmt.Sprintf("crdt:nonce:%s", nonce)
        
        // Store the update
        err := immuClient.Create(key, msg)
        if err != nil {
            log.Error().Err(err).Str("nonce", nonce).Msg("Failed to store message in ImmuDB")
            return
        }
        
        // Also update a set of all nonces
        err = updateNonceSet(nonce)
        if err != nil {
            log.Error().Err(err).Str("nonce", nonce).Msg("Failed to update nonce set")
        }
        
        log.Info().Str("nonce", nonce).Str("type", msg.Type).Msg("Successfully stored message in ImmuDB")
    }()
}

// updateNonceSet adds a nonce to the grow-only set in ImmuDB
func updateNonceSet(nonce string) error {
    const setKey = "crdt:nonce_set"
    
    // Try to get the current set
    var nonceSet map[string]bool
    err := immuClient.ReadJSON(setKey, &nonceSet)
    
    // If not found or error, start with empty set
    if err != nil {
        nonceSet = make(map[string]bool)
    }
    
    // Add the new nonce (idempotent operation)
    nonceSet[nonce] = true
    
    // Store the updated set
    return immuClient.Create(setKey, nonceSet)
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
    var msg BlockMessage
    if err := json.Unmarshal(messageBytes, &msg); err != nil {
        log.Error().Err(err).Msg("Failed to unmarshal message")
        return
    }
    
    // Check if we've already processed this nonce
    if isNonceProcessed(msg.Nonce) {
        log.Debug().Str("nonce", msg.Nonce).Msg("Duplicate nonce received, timing out peer")
        timeoutPeer(remotePeer, 20*time.Second)
        return
    }
    
    // Mark nonce as processed
    markNonceProcessed(msg.Nonce)
    
    // Process the message - update our CRDT state in ImmuDB
    storeNonceInImmuDB(msg.Nonce, msg)
    
    // Log receipt based on message type
    if msg.Type == "transaction" {
        fmt.Printf("\n[TRANSACTION from %s] Nonce: %s\n>>> ", msg.Sender, msg.Nonce)
    } else {
        fmt.Printf("\n[BLOCK from %s] Nonce: %s\n>>> ", msg.Sender, msg.Nonce)
    }
    
    // Notify explorer
    notifyExplorer(msg)
    
    // Only rebroadcast if we haven't reached max hops
    if msg.Hops < config.MaxHops {
        // Forward to our peers
        msg.Hops++
        localPeer := stream.Conn().LocalPeer().String()
        log.Info().
            Str("msg_id", msg.ID).
            Str("nonce", msg.Nonce).
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
            Str("nonce", msg.Nonce).
            Str("type", msg.Type).
            Int("hops", msg.Hops).
            Msg("Max hops reached, not propagating message")
    }
}

// forwardBlock sends the block message to all connected peers
func forwardBlock(h host.Host, msg BlockMessage) {
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
        Str("nonce", msg.Nonce).
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
    msg := BlockMessage{
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
func PropagateTransaction(h host.Host, tx *Block.Transaction, txHash string) error {
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
    msg := BlockMessage{
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
func propagateMessage(h host.Host, msg BlockMessage) error {
    // First, update our own CRDT state
    storeNonceInImmuDB(msg.Nonce, msg)
    
    // Mark this nonce as processed by us
    markNonceProcessed(msg.Nonce)
    
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
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("nonce", msg.Nonce).
        Str("type", msg.Type).
        Int("peers", len(peers)).
        Msg("Starting propagation to peers")
    
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
        Str("nonce", msg.Nonce).
        Str("type", msg.Type).
        Int("success", successCount).
        Int("total", len(peers)).
        Msg("Propagation complete")
    
    notifyExplorer(msg)
    return nil
}

// GetAllNonces retrieves all nonces from the CRDT set
func GetAllNonces() ([]string, error) {
    const setKey = "crdt:nonce_set"
    
    // Try to get the current set
    var nonceSet map[string]bool
    err := immuClient.ReadJSON(setKey, &nonceSet)
    
    if err != nil {
        return nil, fmt.Errorf("failed to read nonce set: %w", err)
    }
    
    // Convert map keys to slice
    nonces := make([]string, 0, len(nonceSet))
    for nonce := range nonceSet {
        nonces = append(nonces, nonce)
    }
    
    return nonces, nil
}

// GetMessageForNonce retrieves the full message for a specific nonce
func GetMessageForNonce(nonce string) (*BlockMessage, error) {
    key := fmt.Sprintf("crdt:nonce:%s", nonce)
    
    var message BlockMessage
    err := immuClient.ReadJSON(key, &message)
    if err != nil {
        return nil, fmt.Errorf("failed to read message data: %w", err)
    }
    
    return &message, nil
}

// GetNonceData retrieves the data for a specific nonce (backward compatibility)
func GetNonceData(nonce string) (map[string]string, error) {
    message, err := GetMessageForNonce(nonce)
    if err != nil {
        return nil, err
    }
    
    // Return the data map
    return message.Data, nil
}