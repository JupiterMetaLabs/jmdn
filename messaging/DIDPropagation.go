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
	"gossipnode/metrics"
)

// DIDMessage represents a message for DID propagation
type DIDMessage struct {
    ID        string    `json:"id"`
    Sender    string    `json:"sender"`
    Timestamp int64     `json:"timestamp"`
    Nonce     string    `json:"nonce"`
    Type      string    `json:"type"` // "did_created", "did_updated", etc.
    Hops      int       `json:"hops"`
    DID       string    `json:"did"`
    PublicKey string    `json:"public_key"`
    Balance   string    `json:"balance,omitempty"`
}

// Store for DID message tracking
var (
    didFilter      *bloom.BloomFilter
    accountsClient *config.PooledConnection
    accountsMutex  sync.RWMutex
    didOnce        sync.Once
)

// InitDIDPropagation initializes the DID propagation system
func InitDIDPropagation(existingClient *config.PooledConnection) error {
    fmt.Println("Initializing DID propagation system...")
    var initErr error
    
    didOnce.Do(func() {
        // Initialize the bloom filter for DID messages
        didFilter = bloom.NewWithEstimates(100000, 0.01)
        
        if existingClient != nil {
            // Use the provided client instead of creating a new one
            accountsMutex.Lock()
            accountsClient = existingClient
            accountsMutex.Unlock()
            log.Info().Msg("DID propagation system initialized with existing database client")
        } else {
            // Create accounts database client if none provided
            client, err := DB_OPs.GetAccountsConnection()
            if err != nil {
                initErr = fmt.Errorf("failed to create accounts database client: %w", err)
                return
            }
            
            accountsMutex.Lock()
            accountsClient = client
            accountsMutex.Unlock()
            log.Info().Msg("DID propagation system initialized with new database client")
        }
    })
    
    return initErr
}

// generateDIDMessageID creates a unique ID for a DID message
func generateDIDMessageID(sender, did string) string {
    hasher := sha256.New()
    hasher.Write([]byte(fmt.Sprintf("%s-%s-%d", sender, did)))
    hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
    return hash[:16] // Return first 16 chars for brevity
}

// isDIDMessageProcessed checks if this message has already been processed
func isDIDMessageProcessed(messageID string) bool {
    return didFilter.Test([]byte(messageID))
}

// markDIDMessageProcessed marks a message as processed
func markDIDMessageProcessed(messageID string) {
    didFilter.Add([]byte(messageID))
}

// storeDIDInDB stores the DID document in the accounts database
func storeDIDInDB(msg DIDMessage) {
    // Store in accounts database in a separate goroutine to prevent blocking
    go func() {
        accountsMutex.RLock()
        if accountsClient == nil {
            log.Error().Msg("Accounts client not initialized")
            accountsMutex.RUnlock()
            return
        }
        client := accountsClient
        accountsMutex.RUnlock()
        
        // Create DID document
        didDoc := &DB_OPs.KeyDocument{
            DIDAddress:       msg.DID,
            Address: msg.PublicKey,
            CreatedAt: msg.Timestamp,
            UpdatedAt: time.Now().Unix(),
        }
        
        // Store DID document
        err := DB_OPs.StoreAccount(client, didDoc)
        if err != nil {
            log.Error().Err(err).Str("did", msg.DID).Msg("Failed to store DID in database")
            return
        }
        
        log.Info().Str("did", msg.DID).Msg("Successfully stored DID in database")
        
        // Also update the DID set (CRDT)
        // err = updateDIDSet(client, msg.DID)
        // if err != nil {
        //     log.Error().Err(err).Str("did", msg.DID).Msg("Failed to update DID set")
        // }
    }()
}

// updateDIDSet adds a DID to the grow-only set in accounts database
func updateDIDSet(client *config.PooledConnection, did string) error {
    const setKey = "crdt:did_set"
    
    // Try to get the current set
    var didSet map[string]bool
    err := DB_OPs.ReadJSON(client, setKey, &didSet)
    
    // If not found or error, start with empty set
    if err != nil {
        didSet = make(map[string]bool)
    }
    
    // Add the new DID (idempotent operation)
    didSet[did] = true
    
    // Store the updated set
    return DB_OPs.Create(client, setKey, didSet)
}

// HandleDIDStream processes incoming DID propagation messages
func HandleDIDStream(stream network.Stream) {
    defer stream.Close()
    
    // Get the remote peer
    remotePeer := stream.Conn().RemotePeer().String()
    
    // Record metrics
    metrics.MessagesReceivedCounter.WithLabelValues("did", remotePeer).Inc()
    
    // Read the incoming message
    reader := bufio.NewReader(stream)
    messageBytes, err := reader.ReadBytes('\n')
    if err != nil {
        if err != io.EOF {
            log.Error().Err(err).Str("peer", remotePeer).
                Msg("Error reading DID message")
        }
        return
    }
    
    // Parse the message
    var msg DIDMessage
    if err := json.Unmarshal(messageBytes, &msg); err != nil {
        log.Error().Err(err).Msg("Failed to unmarshal DID message")
        return
    }
    
    // Check if we've already processed this message
    if isDIDMessageProcessed(msg.ID) {
        log.Debug().Str("message_id", msg.ID).Msg("Duplicate DID message received")
        return
    }
    
    // Mark message as processed
    markDIDMessageProcessed(msg.ID)
    
    // Process the message - update our DID database
    storeDIDInDB(msg)
    
    // Log receipt
    fmt.Printf("\n[DID from %s] DID: %s, PublicKey: %s\n>>> ", msg.Sender, msg.DID, msg.PublicKey)
    
    // Only rebroadcast if we haven't reached max hops
    if msg.Hops < config.MaxDIDHops {
        // Forward to our peers
        msg.Hops++
        localPeer := stream.Conn().LocalPeer().String()
        log.Info().
            Str("msg_id", msg.ID).
            Str("type", msg.Type).
            Str("origin", msg.Sender).
            Str("via", localPeer).
            Str("did", msg.DID).
            Int("hops", msg.Hops).
            Msg("Propagating DID message")
        
        // Forward the message to other peers
        if hostInstance := getHostInstance(); hostInstance != nil {
            go forwardDID(hostInstance, msg)
        } else {
            log.Error().Msg("Cannot access host instance for forwarding DID message")
        }
    } else {
        log.Info().
            Str("msg_id", msg.ID).
            Str("type", msg.Type).
            Str("did", msg.DID).
            Int("hops", msg.Hops).
            Msg("Max hops reached, not propagating DID message")
    }
}

// forwardDID sends the DID message to all connected peers
func forwardDID(h host.Host, msg DIDMessage) {
    // Get all connected peers
    peers := h.Network().Peers()
    
    // Convert message to JSON
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        log.Error().Err(err).Msg("Failed to marshal DID message")
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
        
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            // Open a stream to the peer
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            stream, err := h.NewStream(ctx, peer, config.DIDPropagationProtocol)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open DID stream")
                return
            }
            defer stream.Close()
            
            // Write the message
            _, err = stream.Write(msgBytes)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to write DID message")
                return
            }
            
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            // Record metrics
            metrics.MessagesSentCounter.WithLabelValues("did", peer.String()).Inc()
        }(peerID)
    }
    
    // Wait for all sends to complete
    wg.Wait()
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("type", msg.Type).
        Str("did", msg.DID).
        Int("peers", successCount).
        Msg("DID message propagated to peers")
}

// PropagateDID creates and propagates a DID message to the network
func PropagateDID(h host.Host, doc *DB_OPs.Account) error {
    if doc == nil {
        return fmt.Errorf("DID document cannot be nil")
    }
    
    // Determine message type based on document timestamps
    msgType := "did_created"
    if doc.UpdatedAt > doc.CreatedAt {
        // If updated time is greater than created time, this is an update
        msgType = "did_updated"
    }
    
    // Create a DID message
    now := time.Now().Unix()
    msg := DIDMessage{
        Sender:    h.ID().String(),
        Timestamp: now,
        Nonce:     strconv.FormatUint(doc.Nonce, 10),
        DID:       doc.DIDAddress,
        PublicKey: doc.Address,
        Balance:   doc.Balance,
        Type:      msgType,
        Hops:      0,
    }
    
    // Generate a unique ID based on sender, DID and timestamp
    msg.ID = generateDIDMessageID(msg.Sender, doc.DIDAddress)
    
    // First, add/update the DID in our own database
    storeDIDInDB(msg)
    
    // Mark this message as processed by us
    markDIDMessageProcessed(msg.ID)
    
    // Convert to JSON
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("failed to marshal DID message: %w", err)
    }
    msgBytes = append(msgBytes, '\n')
    
    // Get all connected peers
    peers := h.Network().Peers()
    if len(peers) == 0 {
        log.Warn().
            Str("did", doc.DIDAddress).
            Str("type", msgType).
            Msg("No connected peers to propagate DID to")
        return nil // Not an error, just no one to tell
    }
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("did", doc.DIDAddress).
        Str("public_key", doc.Address).
        Str("balance", doc.Balance).
        Str("type", msgType).
        Int("peers", len(peers)).
        Msg("Starting DID propagation to peers")
    
    // Send message to all peers
    var wg sync.WaitGroup
    var successCount int
    var successMutex sync.Mutex
    
    for _, peerID := range peers {
        wg.Add(1)
        go func(peer peer.ID) {
            defer wg.Done()
            
            // Open stream to peer with timeout
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            
            stream, err := h.NewStream(ctx, peer, config.DIDPropagationProtocol)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open stream for DID")
                return
            }
            defer stream.Close()
            
            // Send the message
            _, err = stream.Write(msgBytes)
            if err != nil {
                log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to send DID message")
                return
            }
            
            // Record success
            successMutex.Lock()
            successCount++
            successMutex.Unlock()
            
            // Record metrics
            metrics.MessagesSentCounter.WithLabelValues("did", peer.String()).Inc()
        }(peerID)
    }
    
    // Wait for all sends to complete
    wg.Wait()
    
    log.Info().
        Str("msg_id", msg.ID).
        Str("did", doc.DIDAddress).
        Str("public_key", doc.Address).
        Str("type", msgType).
        Int("success", successCount).
        Int("total", len(peers)).
        Msg("DID propagation complete")
    
    return nil
}

// ListAllDIDs retrieves all known DIDs from the database
func ListAllDIDs(limit int) ([]*DB_OPs.Account, error) {
    accountsMutex.RLock()
    client := accountsClient
    accountsMutex.RUnlock()
    
    if client == nil {
        return nil, fmt.Errorf("accounts client not initialized")
    }
    
    return DB_OPs.ListAllAccounts(client, limit)
}

