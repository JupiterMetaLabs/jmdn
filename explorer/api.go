package explorer

import (
    "fmt"
    "net/http"

    "github.com/gin-gonic/gin"
    "github.com/rs/zerolog/log"

    "gossipnode/DB_OPs"
)

// ImmuDBServer represents the ImmuDB API server
type ImmuDBServer struct {
    client     *DB_OPs.ImmuClient
    router     *gin.Engine
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
        if _, err := fmt.Sscanf(limitParam, "%d", &limit); err != nil {
            limit = 100
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

    var blockData interface{}
    var foundKey string

    for _, key := range keyFormats {
        err := s.client.ReadJSON(key, &blockData)
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
            keyID := key[len("crdt:nonce:"):]
            
            if keyID == blockID {
                err := s.client.ReadJSON(key, &blockData)
                if err == nil {
                    foundKey = key
                    break
                }
            }
        }
    }

    if foundKey == "" {
        c.JSON(http.StatusNotFound, gin.H{"error": "Block not found"})
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "id": blockID,
        "key": foundKey,
        "data": blockData,
    })
}

// listBlocks handles requests to list all blocks
func (s *ImmuDBServer) listBlocks(c *gin.Context) {
    limit := 100
    if limitParam := c.Query("limit"); limitParam != "" {
        if _, err := fmt.Sscanf(limitParam, "%d", &limit); err != nil {
            limit = 100
        }
    }

    log.Debug().Int("limit", limit).Msg("Listing blocks")

    // Get all nonce keys
    keys, err := s.client.GetKeys("crdt:nonce:", limit)
    if err != nil {
        log.Error().Err(err).Msg("Failed to list block keys")
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to list blocks: %v", err)})
        return
    }

    type blockInfo struct {
        ID        string                 `json:"id"`
        Key       string                 `json:"key"`
        Nonce     string                 `json:"nonce,omitempty"`
        Timestamp int64                  `json:"timestamp,omitempty"`
        Sender    string                 `json:"sender,omitempty"`
        Data      map[string]interface{} `json:"data,omitempty"`
    }

    blocks := make([]blockInfo, 0, len(keys))

    for _, key := range keys {
        // Extract block ID from key
        blockID := key[len("crdt:nonce:"):]
        
        // Read block data
        var data struct {
            Nonce     string                 `json:"nonce"`
            Timestamp int64                  `json:"timestamp"`
            Sender    string                 `json:"sender"`
            Data      map[string]interface{} `json:"data"`
        }

        if err := s.client.ReadJSON(key, &data); err != nil {
            log.Debug().Err(err).Str("key", key).Msg("Failed to read block data")
            continue
        }

        blocks = append(blocks, blockInfo{
            ID:        blockID,
            Key:       key,
            Nonce:     data.Nonce,
            Timestamp: data.Timestamp,
            Sender:    data.Sender,
            Data:      data.Data,
        })
    }

    c.JSON(http.StatusOK, gin.H{
        "count": len(blocks),
        "blocks": blocks,
    })
}

// healthCheck handles health check requests
func (s *ImmuDBServer) healthCheck(c *gin.Context) {
    c.JSON(http.StatusOK, gin.H{
        "status": "ok",
        "service": "immudb-api",
    })
}