package SIM

import (
    crand "crypto/rand"
    "encoding/base64"
    "fmt"
    "math/rand"
    "strconv"
    "sync"
    "time"

    "github.com/google/uuid"
    "github.com/libp2p/go-libp2p/core/host"
    "github.com/rs/zerolog/log"

    "gossipnode/messaging"
)

// BlockMessage represents a message for block propagation
// This must match the structure used in messaging/block_propagation.go
type BlockMessage struct {
    ID        string            `json:"id"`        // Unique message ID
    Sender    string            `json:"sender"`    // Original sender's peer ID
    Timestamp int64             `json:"timestamp"` // Unix timestamp when message was created
    Nonce     string            `json:"nonce"`     // Unique nonce for CRDT
    Data      map[string]string `json:"data"`      // Data payload for the CRDT
    Hops      int               `json:"hops"`      // How many hops this message has made
}

// List of demo names for simulation
var (
    senders   = []string{"alice", "bob", "charlie", "dave", "eve", "frank", "grace", "heidi", "ivan", "judy"}
    receivers = []string{"mallory", "niaj", "olivia", "peggy", "quentin", "rupert", "sybil", "trent", "ursula", "victor"}
    mutex     = &sync.Mutex{}
)

// RandomAmount generates a random transaction amount between min and max
func RandomAmount(min, max float64) string {
    mutex.Lock()
    defer mutex.Unlock()
    amount := min + rand.Float64()*(max-min)
    return strconv.FormatFloat(amount, 'f', 2, 64)
}

// RandomSender picks a random sender name
func RandomSender() string {
    mutex.Lock()
    defer mutex.Unlock()
    return senders[rand.Intn(len(senders))]
}

// RandomReceiver picks a random receiver name that is different from sender
func RandomReceiver(sender string) string {
    mutex.Lock()
    defer mutex.Unlock()
    for {
        receiver := receivers[rand.Intn(len(receivers))]
        if receiver != sender {
            return receiver
        }
    }
}

// GenerateRandomBlockData creates random transaction data for simulation
func GenerateRandomBlockData() map[string]string {
    // Generate a unique transaction ID
    txID := fmt.Sprintf("tx-%s", uuid.New().String()[:8])
    
    // Select random sender
    sender := RandomSender()
    
    // Generate random transaction data
    data := map[string]string{
        "transaction_id": txID,
        "amount":         RandomAmount(1.0, 100.0),
        "sender":         sender,
        "receiver":       RandomReceiver(sender),
        "timestamp":      fmt.Sprintf("%d", time.Now().Unix()),
        "type":           "payment",
        "currency":       "ETH",
        "fee":            RandomAmount(0.01, 0.5),
    }
    
    return data
}

// GenerateNonce creates a cryptographically secure random nonce
func GenerateNonce() (string, error) {
    nonceBytes := make([]byte, 16)
    _, err := crand.Read(nonceBytes)
    if err != nil {
        return "", err
    }
    return base64.URLEncoding.EncodeToString(nonceBytes), nil
}

// CreateBlockMessage generates a new block message with random data
func CreateBlockMessage(h host.Host) (*BlockMessage, error) {
    // Generate random data payload
    data := GenerateRandomBlockData()
    
    // Generate a secure nonce
    nonce, err := GenerateNonce()
    if err != nil {
        return nil, fmt.Errorf("failed to generate nonce: %w", err)
    }
    
    // Create timestamp
    now := time.Now().Unix()
    
    // Create the message
    msg := &BlockMessage{
        Sender:    h.ID().String(),
        Timestamp: now,
        Nonce:     nonce,
        Data:      data,
        Hops:      0,
    }
    
    // Generate ID based on content
    msg.ID = generateBlockID(msg.Sender, msg.Nonce, msg.Timestamp)
    
    return msg, nil
}

// generateBlockID creates a deterministic ID for a block message
func generateBlockID(sender, nonce string, timestamp int64) string {
    idStr := fmt.Sprintf("%s-%s-%d", sender, nonce, timestamp)
    return fmt.Sprintf("block-%x", idStr[:16])
}

// PropagateRandomBlock generates and propagates a random block
func PropagateRandomBlock(h host.Host) error {
    // Create a new block message
    block, err := CreateBlockMessage(h)
    if err != nil {
        return fmt.Errorf("failed to create block message: %w", err)
    }
    
    // Log the transaction details
    log.Info().
        Str("block_id", block.ID).
        Str("tx_id", block.Data["transaction_id"]).
        Str("amount", block.Data["amount"]).
        Str("sender", block.Data["sender"]).
        Str("receiver", block.Data["receiver"]).
        Str("nonce", block.Nonce).
        Msg("Propagating random block")
    
    // Propagate the block to all peers - pass just the data map
    err = messaging.PropagateBlock(h, block.Data)
    if err != nil {
        return fmt.Errorf("failed to propagate block: %w", err)
    }
    
    fmt.Printf("\nBlock propagated: %s\n", block.ID)
    fmt.Printf("Transaction: %s sent %s %s to %s\n", 
        block.Data["sender"], 
        block.Data["amount"], 
        block.Data["currency"], 
        block.Data["receiver"])
    
    return nil
}

// StartBlockSimulation starts simulating block propagation at random intervals
func StartBlockSimulation(h host.Host, minInterval, maxInterval time.Duration) {
    // Seed the random number generator
    rand.Seed(time.Now().UnixNano())
    
    log.Info().
        Dur("min_interval", minInterval).
        Dur("max_interval", maxInterval).
        Msg("Starting block simulation")
    
    go func() {
        for {
            // Wait for a random interval
            interval := minInterval + time.Duration(rand.Int63n(int64(maxInterval-minInterval)))
            time.Sleep(interval)
            
            // Generate and propagate a random block
            err := PropagateRandomBlock(h)
            if err != nil {
                log.Error().Err(err).Msg("Block simulation error")
            }
        }
    }()
}

// RunOneTimeSimulation runs a fixed number of block propagations
func RunOneTimeSimulation(h host.Host, count int) error {
    log.Info().Int("count", count).Msg("Running one-time block simulation")
    
    // Seed the random number generator
    rand.Seed(time.Now().UnixNano())
    
    for i := 0; i < count; i++ {
        fmt.Printf("\nPropagating block %d of %d\n", i+1, count)
        
        err := PropagateRandomBlock(h)
        if err != nil {
            return err
        }
        
        // Small delay between propagations to avoid overwhelming the network
        time.Sleep(500 * time.Millisecond)
    }
    
    return nil
}

// SimulateNetworkStress simulates high load on the network
func SimulateNetworkStress(h host.Host, blocksPerSecond, durationSeconds int) error {
    log.Info().
        Int("blocks_per_second", blocksPerSecond).
        Int("duration_seconds", durationSeconds).
        Msg("Starting network stress test")
    
    // Calculate delay between blocks
    delay := time.Second / time.Duration(blocksPerSecond)
    totalBlocks := blocksPerSecond * durationSeconds
    
    fmt.Printf("Starting stress test: %d blocks over %d seconds\n", 
        totalBlocks, durationSeconds)
    
    // Track start time for progress updates
    startTime := time.Now()
    endTime := startTime.Add(time.Duration(durationSeconds) * time.Second)
    
    // Launch blocks at the specified rate
    for i := 0; i < totalBlocks; i++ {
        // Check if we've exceeded the duration
        if time.Now().After(endTime) {
            break
        }
        
        // Show periodic progress
        if i%blocksPerSecond == 0 && i > 0 {
            elapsed := time.Since(startTime).Seconds()
            fmt.Printf("Progress: %d blocks sent (%.1f seconds elapsed)\n", 
                i, elapsed)
        }
        
        // Generate and propagate block
        err := PropagateRandomBlock(h)
        if err != nil {
            log.Error().Err(err).Int("block_num", i).Msg("Stress test block error")
        }
        
        // Wait for the calculated delay
        time.Sleep(delay)
    }
    
    // Report completion
    actualDuration := time.Since(startTime)
    fmt.Printf("\nStress test completed: %d blocks sent in %.1f seconds\n", 
        totalBlocks, actualDuration.Seconds())
    
    return nil
}