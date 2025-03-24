package explorer

import (
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/rs/zerolog/log"

    "gossipnode/DB_OPs"
    "gossipnode/config"
)

// ImmuDBServer represents the ImmuDB API server
type ImmuDBServer struct {
    client     *DB_OPs.ImmuClient
    router     *gin.Engine
}

// BlockMessage represents the stored block message format
type BlockMessage struct {
    ID          string              `json:"id"`
    Sender      string              `json:"sender"`
    Timestamp   int64               `json:"timestamp"`
    Nonce       string              `json:"nonce"`
    Data        map[string]string   `json:"data,omitempty"`
    Transaction *config.Transaction  `json:"transaction,omitempty"`
    Type        string              `json:"type"`
    Hops        int                 `json:"hops"`
}

// TransactionResponse represents a transaction API response
type TransactionResponse struct {
    Hash        string                 `json:"hash"`
    From        string                 `json:"from,omitempty"`
    To          string                 `json:"to,omitempty"`
    Value       string                 `json:"value,omitempty"`
    Type        string                 `json:"type"`
    Timestamp   time.Time              `json:"timestamp"`
    ChainID     string                 `json:"chain_id,omitempty"`
    Nonce       string                 `json:"nonce,omitempty"`
    GasLimit    string                 `json:"gas_limit,omitempty"`
    GasPrice    string                 `json:"gas_price,omitempty"`
    MaxFee      string                 `json:"max_fee,omitempty"`
    MaxPriority string                 `json:"max_priority_fee,omitempty"`
    Data        string                 `json:"data,omitempty"`
    RawData     map[string]interface{} `json:"raw_data,omitempty"`
}

// BlockResponse represents a block API response
type BlockResponse struct {
    ID          string                 `json:"id"`
    Nonce       string                 `json:"nonce"`
    Timestamp   time.Time              `json:"timestamp"`
    Sender      string                 `json:"sender"`
    Type        string                 `json:"type"`
    Hops        int                    `json:"hops"`
    RawData     map[string]interface{} `json:"data,omitempty"`
}

// NewImmuDBServer creates a new ImmuDB API server
func NewImmuDBServer() (*ImmuDBServer, error) {
    // Create ImmuDB client
    client, err := DB_OPs.New()
    if err != nil {
        return nil, fmt.Errorf("failed to create ImmuDB client: %w", err)
    }

    // Create gin router with default middleware
    router := gin.Default()

    // Create server instance
    server := &ImmuDBServer{
        client: client,
        router: router,
    }

    // Set up routes
    server.setupRoutes()

    return server, nil
}

// setupRoutes configures the API routes
func (s *ImmuDBServer) setupRoutes() {
    // Add CORS middleware
    s.router.Use(cors())

    // API routes
    api := s.router.Group("/api")
    {
        // Get value for a specific key
        api.GET("/keys/:key", s.getKeyValue)
        
        // Get all keys with a certain prefix
        api.GET("/keys", s.listKeys)
        
        // Get specific block by ID
        api.GET("/blocks/:id", s.getBlock)
        
        // List all blocks
        api.GET("/blocks", s.listBlocks)
        
        // Get transaction by hash
        api.GET("/transactions/:hash", s.getTransaction)
        
        // List all transactions
        api.GET("/transactions", s.listTransactions)
        
        // Get blockchain stats
        api.GET("/stats", s.getStats)
        
        // Health check
        api.GET("/health", s.healthCheck)
    }
}

// Start runs the HTTP server
func (s *ImmuDBServer) Start(addr string) error {
    log.Info().Str("addr", addr).Msg("Starting ImmuDB API server")
    return s.router.Run(addr)
}

// Close cleans up resources
func (s *ImmuDBServer) Close() {
    if s.client != nil {
        s.client.Close()
    }
}

// CORS middleware
func cors() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Authorization")

        if c.Request.Method == "OPTIONS" {
            c.AbortWithStatus(http.StatusNoContent)
            return
        }

        c.Next()
    }
}

// getKeyValue handles requests to get a value for a specific key
func (s *ImmuDBServer) getKeyValue(c *gin.Context) {
    key := c.Param("key")
    if key == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Key parameter is required"})
        return
    }

    log.Debug().Str("key", key).Msg("Getting value for key")

    // Read raw value from ImmuDB
    value, err := s.client.Read(key)
    if err != nil {
        log.Error().Err(err).Str("key", key).Msg("Failed to read key")
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Key not found: %v", err)})
        return
    }

    // Try to parse as JSON and return structured data if possible
    var jsonData interface{}
    if err := s.client.ReadJSON(key, &jsonData); err == nil {
        c.JSON(http.StatusOK, gin.H{
            "key": key,
            "data": jsonData,
        })
    } else {
        // Return as string if not valid JSON
        c.JSON(http.StatusOK, gin.H{
            "key": key,
            "data": string(value),
        })
    }
}

// listKeys handles requests to list keys with a certain prefix
func (s *ImmuDBServer) listKeys(c *gin.Context) {
    prefix := c.Query("prefix")
    if prefix == "" {
        prefix = "crdt:" // Default to CRDT keys
    }

    limit := 100
    if limitParam := c.Query("limit"); limitParam != "" {
        if parsed, err := strconv.Atoi(limitParam); err == nil && parsed > 0 {
            limit = parsed
        }
    }

    log.Debug().Str("prefix", prefix).Int("limit", limit).Msg("Listing keys")

    // Get keys from ImmuDB
    keys, err := s.client.GetKeys(prefix, limit)
    if err != nil {
        log.Error().Err(err).Str("prefix", prefix).Msg("Failed to list keys")
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to list keys: %v", err)})
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "prefix": prefix,
        "count": len(keys),
        "keys": keys,
    })
}

// getBlock handles requests to get a specific block by ID
func (s *ImmuDBServer) getBlock(c *gin.Context) {
    blockID := c.Param("id")
    if blockID == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Block ID parameter is required"})
        return
    }

    log.Debug().Str("block_id", blockID).Msg("Getting block")

    // Try different key formats
    keyFormats := []string{
        fmt.Sprintf("crdt:nonce:%s", blockID),
        blockID,
    }

    var blockMsg BlockMessage
    var foundKey string

    for _, key := range keyFormats {
        err := s.client.ReadJSON(key, &blockMsg)
        if err == nil {
            foundKey = key
            break
        }
    }

    if foundKey == "" {
        // If direct lookup failed, try listing all keys and searching
        keys, err := s.client.GetKeys("crdt:nonce:", 1000)
        if err != nil {
            log.Error().Err(err).Msg("Failed to list keys")
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search for block"})
            return
        }

        // Search for block ID in keys
        for _, key := range keys {
            // Extract the ID portion
            if strings.HasPrefix(key, "crdt:nonce:") {
                keyID := key[len("crdt:nonce:"):]
                
                if keyID == blockID {
                    err := s.client.ReadJSON(key, &blockMsg)
                    if err == nil {
                        foundKey = key
                        break
                    }
                }
            }
        }
    }

    if foundKey == "" {
        c.JSON(http.StatusNotFound, gin.H{"error": "Block not found"})
        return
    }

    // Convert to user-friendly response
    response := BlockResponse{
        ID:        blockMsg.ID,
        Nonce:     blockMsg.Nonce,
        Timestamp: time.Unix(blockMsg.Timestamp, 0),
        Sender:    blockMsg.Sender,
        Type:      blockMsg.Type,
        Hops:      blockMsg.Hops,
    }

    // Handle data based on block type
    if blockMsg.Data != nil {
        // Convert string map to interface map for better JSON representation
        rawData := make(map[string]interface{})
        for k, v := range blockMsg.Data {
            // Try to unmarshal nested JSON if possible
            var nestedData interface{}
            if err := json.Unmarshal([]byte(v), &nestedData); err == nil {
                rawData[k] = nestedData
            } else {
                rawData[k] = v
            }
        }
        response.RawData = rawData
    }

    c.JSON(http.StatusOK, response)
}

// listBlocks handles requests to list all blocks
func (s *ImmuDBServer) listBlocks(c *gin.Context) {
    limit := 100
    if limitParam := c.Query("limit"); limitParam != "" {
        if parsed, err := strconv.Atoi(limitParam); err == nil && parsed > 0 {
            limit = parsed
        }
    }

    // Get block type filter
    blockType := c.Query("type")
    if blockType != "" && blockType != "block" && blockType != "transaction" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid block type filter. Use 'block' or 'transaction'"})
        return
    }

    log.Debug().Int("limit", limit).Str("type", blockType).Msg("Listing blocks")

    // Get all nonce keys
    keys, err := s.client.GetKeys("crdt:nonce:", limit)
    if err != nil {
        log.Error().Err(err).Msg("Failed to list block keys")
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to list blocks: %v", err)})
        return
    }

    blocks := make([]BlockResponse, 0, len(keys))

    for _, key := range keys {
        // Extract block ID from key        
        // Read block message
        var blockMsg BlockMessage
        if err := s.client.ReadJSON(key, &blockMsg); err != nil {
            log.Debug().Err(err).Str("key", key).Msg("Failed to read block data")
            continue
        }

        // Apply type filter if set
        if blockType != "" && blockMsg.Type != blockType {
            continue
        }

        // Convert to response format
        response := BlockResponse{
            ID:        blockMsg.ID,
            Nonce:     blockMsg.Nonce,
            Timestamp: time.Unix(blockMsg.Timestamp, 0),
            Sender:    blockMsg.Sender,
            Type:      blockMsg.Type,
            Hops:      blockMsg.Hops,
        }

        // Handle data based on block type
        if blockMsg.Data != nil {
            // Convert string map to interface map for better JSON representation
            rawData := make(map[string]interface{})
            for k, v := range blockMsg.Data {
                // Try to unmarshal nested JSON if possible
                var nestedData interface{}
                if err := json.Unmarshal([]byte(v), &nestedData); err == nil {
                    rawData[k] = nestedData
                } else {
                    rawData[k] = v
                }
            }
            response.RawData = rawData
        }

        blocks = append(blocks, response)
    }

    c.JSON(http.StatusOK, gin.H{
        "count": len(blocks),
        "blocks": blocks,
    })
}

// getTransaction handles requests to get a transaction by hash
func (s *ImmuDBServer) getTransaction(c *gin.Context) {
    txHash := c.Param("hash")
    if txHash == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction hash parameter is required"})
        return
    }

    log.Debug().Str("tx_hash", txHash).Msg("Getting transaction")

    // Try different key formats
    keyFormats := []string{
        fmt.Sprintf("tx:%s", txHash),
    }

    var txMsg BlockMessage
    var foundKey string

    for _, key := range keyFormats {
        err := s.client.ReadJSON(key, &txMsg)
        if err == nil {
            foundKey = key
            break
        }
    }

    if foundKey == "" {
        // If direct lookup failed, check in all blocks for transaction_hash field
        keys, err := s.client.GetKeys("crdt:nonce:", 1000)
        if err != nil {
            log.Error().Err(err).Msg("Failed to list keys")
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search for transaction"})
            return
        }

        // Search for transaction hash in block data
        for _, key := range keys {
            var blockMsg BlockMessage
            if err := s.client.ReadJSON(key, &blockMsg); err != nil {
                continue
            }

            // Check if this block contains our transaction
            if blockMsg.Type == "transaction" && blockMsg.Data != nil {
                if hash, ok := blockMsg.Data["transaction_hash"]; ok && hash == txHash {
                    txMsg = blockMsg
                    foundKey = key
                    break
                }
            }
        }
    }

    if foundKey == "" {
        c.JSON(http.StatusNotFound, gin.H{"error": "Transaction not found"})
        return
    }

    // Convert to user-friendly response
    response := createTransactionResponse(txMsg)

    c.JSON(http.StatusOK, response)
}

// listTransactions handles requests to list all transactions
func (s *ImmuDBServer) listTransactions(c *gin.Context) {
    limit := 100
    if limitParam := c.Query("limit"); limitParam != "" {
        if parsed, err := strconv.Atoi(limitParam); err == nil && parsed > 0 {
            limit = parsed
        }
    }

    log.Debug().Int("limit", limit).Msg("Listing transactions")

    // Try first to get transaction-specific keys
    txKeys, _ := s.client.GetKeys("tx:", limit)
    
    // Also check regular blocks for transaction types
    blockKeys, err := s.client.GetKeys("crdt:nonce:", limit)
    if err != nil {
        log.Error().Err(err).Msg("Failed to list block keys")
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to list transactions: %v", err)})
        return
    }

    transactions := make([]TransactionResponse, 0, limit)

    // First process dedicated transaction keys
    for _, key := range txKeys {
        var txMsg BlockMessage
        if err := s.client.ReadJSON(key, &txMsg); err != nil {
            continue
        }

        response := createTransactionResponse(txMsg)
        transactions = append(transactions, response)
        
        // Respect the limit
        if len(transactions) >= limit {
            break
        }
    }

    // If we didn't reach the limit, also check block keys for transactions
    if len(transactions) < limit {
        for _, key := range blockKeys {
            var blockMsg BlockMessage
            if err := s.client.ReadJSON(key, &blockMsg); err != nil {
                continue
            }

            // Only process transaction type blocks
            if blockMsg.Type == "transaction" {
                response := createTransactionResponse(blockMsg)
                transactions = append(transactions, response)
                
                // Respect the limit
                if len(transactions) >= limit {
                    break
                }
            }
        }
    }

    c.JSON(http.StatusOK, gin.H{
        "count": len(transactions),
        "transactions": transactions,
    })
}

// createTransactionResponse converts a BlockMessage to a TransactionResponse
func createTransactionResponse(msg BlockMessage) TransactionResponse {
    response := TransactionResponse{
        Timestamp: time.Unix(msg.Timestamp, 0),
        Type:      "unknown",
    }

    // Get transaction hash
    if msg.Data != nil {
        if hash, ok := msg.Data["transaction_hash"]; ok {
            response.Hash = hash
        }
    }

    // Extract transaction details
    if msg.Transaction != nil {
        tx := msg.Transaction
        
        // Set transaction type
        if tx.MaxFeePerGas != nil && tx.MaxPriorityFeePerGas != nil {
            response.Type = "eip1559"
        } else if tx.GasPrice != nil {
            response.Type = "legacy"
        }
        
        // Set common fields
        if tx.ChainID != nil {
            response.ChainID = tx.ChainID.String()
        }
        
        if tx.Nonce != 0 {
            response.Nonce = string(tx.Nonce)
        }
        
        if tx.GasLimit != 0 {
            response.GasLimit = strconv.FormatUint(tx.GasLimit, 10)
        }
        
        if tx.Value != nil {
            response.Value = tx.Value.String()
        }
        
        if tx.GasPrice != nil {
            response.GasPrice = tx.GasPrice.String()
        }
        
        if tx.MaxFeePerGas != nil {
            response.MaxFee = tx.MaxFeePerGas.String()
        }
        
        if tx.MaxPriorityFeePerGas != nil {
            response.MaxPriority = tx.MaxPriorityFeePerGas.String()
        }
        
        // Set addresses
        response.From = msg.Sender // Default to message sender
        
        if tx.To != nil {
            response.To = tx.To.Hex()
        }
        
        // Set data if available
        if tx.Data != nil && len(tx.Data) > 0 {
            response.Data = string(tx.Data)
        }
    } else if msg.Data != nil {
        // Fallback to data fields if structured transaction isn't available
        response.RawData = make(map[string]interface{})
        for k, v := range msg.Data {
            // Try to unmarshal nested JSON if possible
            var nestedData interface{}
            if err := json.Unmarshal([]byte(v), &nestedData); err == nil {
                response.RawData[k] = nestedData
            } else {
                response.RawData[k] = v
            }
        }
    }

    return response
}

// getStats handles requests for blockchain statistics
func (s *ImmuDBServer) getStats(c *gin.Context) {
    // Get database state
    dbState, err := s.client.GetDatabaseState()
    if err != nil {
        log.Error().Err(err).Msg("Failed to get database state")
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get database state"})
        return
    }

    // Count transactions
    txKeyCount, _ := s.countKeysByPrefix("tx:")
    
    // Count blocks
    blockCount, _ := s.countKeysByPrefix("crdt:nonce:")
    
    // Get total key count
    totalKeys, _ := s.countAllKeys()

    // Return stats
    c.JSON(http.StatusOK, gin.H{
        "database": gin.H{
            "tx_id": dbState.TxId,
            "merkle_root": fmt.Sprintf("%x", dbState.TxHash),
        },
        "transactions": txKeyCount,
        "blocks": blockCount,
        "total_keys": totalKeys,
        "last_update": time.Now().UTC(),
    })
}

// countKeysByPrefix counts keys with a specific prefix
func (s *ImmuDBServer) countKeysByPrefix(prefix string) (int, error) {
    // Use pagination to count all keys
    const batchSize = 1000
    var totalCount int
    lastKey := prefix
    hasMore := true

    for hasMore {
        keys, err := s.client.GetKeys(lastKey, batchSize)
        if err != nil {
            return totalCount, err
        }

        // Count keys that match our prefix
        for _, key := range keys {
            if strings.HasPrefix(key, prefix) {
                totalCount++
            }
        }

        // Check if we need to continue
        if len(keys) < batchSize {
            hasMore = false
        } else if len(keys) > 0 {
            lastKey = keys[len(keys)-1]
        } else {
            hasMore = false
        }
    }

    return totalCount, nil
}

// countAllKeys counts all keys in the database
func (s *ImmuDBServer) countAllKeys() (int, error) {
    // Use pagination to count all keys
    const batchSize = 1000
    var totalCount int
    var lastKey string
    hasMore := true

    for hasMore {
        keys, err := s.client.GetKeys(lastKey, batchSize)
        if err != nil {
            return totalCount, err
        }

        totalCount += len(keys)

        // Check if we need to continue
        if len(keys) < batchSize {
            hasMore = false
        } else if len(keys) > 0 {
            lastKey = keys[len(keys)-1]
        } else {
            hasMore = false
        }
    }

    return totalCount, nil
}

// healthCheck handles health check requests
func (s *ImmuDBServer) healthCheck(c *gin.Context) {
    isHealthy := s.client.IsHealthy()
    
    var status string
    var statusCode int
    
    if isHealthy {
        status = "ok"
        statusCode = http.StatusOK
    } else {
        status = "error"
        statusCode = http.StatusServiceUnavailable
    }
    
    c.JSON(statusCode, gin.H{
        "status": status,
        "service": "immudb-api",
        "timestamp": time.Now().UTC(),
    })
}