package explorer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	// "strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
    ReadBufferSize:  1024,
    WriteBufferSize: 1024,
    CheckOrigin: func(r *http.Request) bool {
        // In production, check the origin
        return true // Allow any origin for development
    },
}

// Block represents a block in the blockchain explorer
type Block struct {
    ID        string            `json:"id"`
    Nonce     string            `json:"nonce"`
    Timestamp int64             `json:"timestamp"`
    Sender    string            `json:"sender"`
    Data      map[string]string `json:"data"`
    TxID      uint64            `json:"txId"`
}

// Dashboard represents the summary data for the explorer dashboard
type Dashboard struct {
    BlockCount   int     `json:"blockCount"`
    NodeCount    int     `json:"nodeCount"`
    TotalAmount  float64 `json:"totalAmount"`
    LatestBlocks []Block `json:"latestBlocks"`
    LatestTxID   uint64  `json:"latestTxId"`
}

// Client represents a WebSocket client connection
type Client struct {
    ID       string
    Conn     *websocket.Conn
    Send     chan []byte
    Explorer *Explorer
}

// Explorer represents the block explorer with WebSocket functionality
type Explorer struct {
    ImmuClient       *DB_OPs.ImmuClient
    Clients          map[string]*Client
    Register         chan *Client
    Unregister       chan *Client
    Broadcast        chan []byte
    Dashboard        Dashboard
    mutex            sync.RWMutex
    LastCheckedTxID  uint64
    UpdateInterval   time.Duration
    hasher           *DB_OPs.BlockHasher
    processingBlocks map[string]bool
    blocksCache      []Block
    maxCacheSize     int
}

// NewExplorer creates a new Explorer instance
func NewExplorer() (*Explorer, error) {
    // Create ImmuDB client
    immuClient, err := DB_OPs.New()
    if err != nil {
        return nil, err
    }

    // Create explorer
    explorer := &Explorer{
        ImmuClient:       immuClient,
        Clients:          make(map[string]*Client),
        Register:         make(chan *Client),
        Unregister:       make(chan *Client),
        Broadcast:        make(chan []byte),
        UpdateInterval:   5 * time.Second,
        processingBlocks: make(map[string]bool),
        blocksCache:      make([]Block, 0, 100),
        maxCacheSize:     100,
        hasher:           DB_OPs.NewBlockHasher(),
    }

    // Initialize dashboard data
    err = explorer.updateDashboard()
    if err != nil {
        immuClient.Close()
        return nil, err
    }

    // Start processing loops
    go explorer.run()
    go explorer.monitorBlocks()

    return explorer, nil
}

// run processes WebSocket operations
func (e *Explorer) run() {
    for {
        select {
        case client := <-e.Register:
            e.Clients[client.ID] = client
            log.Info().Str("client_id", client.ID).Msg("Client connected to explorer")

            // Send initial dashboard data
            e.mutex.RLock()
            dashboardJSON, err := json.Marshal(e.Dashboard)
            e.mutex.RUnlock()

            if err == nil {
                message := map[string]interface{}{
                    "type": "dashboard",
                    "data": json.RawMessage(dashboardJSON),
                }
                messageJSON, _ := json.Marshal(message)
                client.Send <- messageJSON
            }

        case client := <-e.Unregister:
            if _, ok := e.Clients[client.ID]; ok {
                delete(e.Clients, client.ID)
                close(client.Send)
                log.Info().Str("client_id", client.ID).Msg("Client disconnected from explorer")
            }

        case message := <-e.Broadcast:
            for id, client := range e.Clients {
                select {
                case client.Send <- message:
                default:
                    close(client.Send)
                    delete(e.Clients, id)
                }
            }
        }
    }
}

// monitorBlocks periodically checks for new blocks in ImmuDB
func (e *Explorer) monitorBlocks() {
    ticker := time.NewTicker(e.UpdateInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            err := e.checkForUpdates()
            if err != nil {
                log.Error().Err(err).Msg("Failed to check for updates")
            }
        }
    }
}

// checkForUpdates looks for new blocks and updates the dashboard
func (e *Explorer) checkForUpdates() error {
    // Get current state from ImmuDB to check for new transactions
    state, err := e.ImmuClient.GetDatabaseState()
    if err != nil {
        return err
    }

    // If no new transactions, nothing to do
    if state.TxId <= e.LastCheckedTxID {
        return nil
    }

    // Update the dashboard with new data
    err = e.updateDashboard()
    if err != nil {
        return err
    }

    // Broadcast updated dashboard to all clients
    e.mutex.RLock()
    dashboardJSON, err := json.Marshal(e.Dashboard)
    e.mutex.RUnlock()

    if err != nil {
        return err
    }

    message := map[string]interface{}{
        "type": "dashboard",
        "data": json.RawMessage(dashboardJSON),
    }

    messageJSON, err := json.Marshal(message)
    if err != nil {
        return err
    }

    e.Broadcast <- messageJSON
    return nil
}

// updateDashboard refreshes the dashboard data from ImmuDB
func (e *Explorer) updateDashboard() error {
    e.mutex.Lock()
    defer e.mutex.Unlock()

    // Get all nonce keys with the CRDT prefix
    nonceKeys, err := e.ImmuClient.GetKeys("crdt:nonce:", config.DefaultScanLimit)
    if err != nil {
        return err
    }

    // Track found nodes and total amount
    nodes := make(map[string]bool)
    var totalAmount float64

    // Process each block
    latestBlocks := make([]Block, 0, 10)
    for _, key := range nonceKeys {
        // Skip if we're already processing this block
        nonce := key[len("crdt:nonce:"):]
        if e.processingBlocks[nonce] {
            continue
        }

        // Mark as processing
        e.processingBlocks[nonce] = true

        // Get block data
        var blockData struct {
            Nonce     string            `json:"nonce"`
            Data      map[string]string `json:"data"`
            Sender    string            `json:"sender"`
            Timestamp int64             `json:"timestamp"`
        }

        err = e.ImmuClient.ReadJSON(key, &blockData)
        if err != nil {
            log.Error().Err(err).Str("key", key).Msg("Failed to read block data")
            delete(e.processingBlocks, nonce)
            continue
        }

        // Create block for dashboard
        block := Block{
            ID:        e.hasher.HashBlock(blockData.Nonce, blockData.Sender, blockData.Timestamp),
            Nonce:     blockData.Nonce,
            Timestamp: blockData.Timestamp,
            Sender:    blockData.Sender,
            Data:      blockData.Data,
        }

        // Add to latest blocks if newer than what we have
        if len(latestBlocks) < 10 {
            latestBlocks = append(latestBlocks, block)
        }

        // Track unique nodes
        nodes[blockData.Sender] = true

        // Add to total amount if it's a payment
        if amount, ok := blockData.Data["amount"]; ok {
            if amountFloat, err := json.Number(amount).Float64(); err == nil {
                totalAmount += amountFloat
            }
        }

        // Add to cache
        e.addToBlockCache(block)
    }

    // Update dashboard
    state, err := e.ImmuClient.GetDatabaseState()
    if err != nil {
        return err
    }

    e.Dashboard = Dashboard{
        BlockCount:   len(nonceKeys),
        NodeCount:    len(nodes),
        TotalAmount:  totalAmount,
        LatestBlocks: latestBlocks,
        LatestTxID:   state.TxId,
    }

    // Update last checked transaction ID
    e.LastCheckedTxID = state.TxId

    return nil
}

// addToBlockCache adds a block to the in-memory cache
func (e *Explorer) addToBlockCache(block Block) {
    // Check if already in cache
    for _, b := range e.blocksCache {
        if b.ID == block.ID {
            return
        }
    }

    // Add to front of cache
    e.blocksCache = append([]Block{block}, e.blocksCache...)

    // Trim if needed
    if len(e.blocksCache) > e.maxCacheSize {
        e.blocksCache = e.blocksCache[:e.maxCacheSize]
    }
}

// GetBlocks returns blocks from the cache with pagination
func (e *Explorer) GetBlocks(offset, limit int) []Block {
    e.mutex.RLock()
    defer e.mutex.RUnlock()

    if offset >= len(e.blocksCache) {
        return []Block{}
    }

    end := offset + limit
    if end > len(e.blocksCache) {
        end = len(e.blocksCache)
    }

    return e.blocksCache[offset:end]
}

// GetBlockByID returns a specific block by ID
func (e *Explorer) GetBlockByID(id string) (Block, bool) {
    e.mutex.RLock()
    defer e.mutex.RUnlock()

    for _, block := range e.blocksCache {
        if block.ID == id {
            return block, true
        }
    }

    return Block{}, false
}

// GetDashboard returns the current dashboard data
func (e *Explorer) GetDashboard() Dashboard {
    e.mutex.RLock()
    defer e.mutex.RUnlock()
    return e.Dashboard
}

// handleWebSocket handles WebSocket connections
func (e *Explorer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Error().Err(err).Msg("Failed to set up WebSocket connection")
        return
    }

    clientID := r.RemoteAddr + ":" + time.Now().String()
    client := &Client{
        ID:       clientID,
        Conn:     conn,
        Send:     make(chan []byte, 256),
        Explorer: e,
    }

    // Register the client
    e.Register <- client

    // Start goroutines for reading and writing
    go client.readPump()
    go client.writePump()
}

// readPump pumps messages from the WebSocket to the hub
func (c *Client) readPump() {
    defer func() {
        c.Explorer.Unregister <- c
        c.Conn.Close()
    }()

    c.Conn.SetReadLimit(1024)
    c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
    c.Conn.SetPongHandler(func(string) error {
        c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
        return nil
    })

    for {
        _, _, err := c.Conn.ReadMessage()
        if err != nil {
            if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
                log.Error().Err(err).Msg("WebSocket read error")
            }
            break
        }
        // We're not processing incoming messages for now
    }
}

// writePump pumps messages from the hub to the WebSocket connection
func (c *Client) writePump() {
    ticker := time.NewTicker(54 * time.Second)
    defer func() {
        ticker.Stop()
        c.Conn.Close()
    }()

    for {
        select {
        case message, ok := <-c.Send:
            c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
            if !ok {
                // The hub closed the channel
                c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
                return
            }

            w, err := c.Conn.NextWriter(websocket.TextMessage)
            if err != nil {
                return
            }
            w.Write(message)

            if err := w.Close(); err != nil {
                return
            }
        case <-ticker.C:
            c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
            if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                return
            }
        }
    }
}

// SetupRoutes sets up the HTTP routes for the explorer
func (e *Explorer) SetupRoutes() *mux.Router {
    r := mux.NewRouter()

    // WebSocket endpoint
    r.HandleFunc("/ws", e.handleWebSocket)

    // API endpoints
    api := r.PathPrefix("/api").Subrouter()
    api.HandleFunc("/dashboard", e.handleGetDashboard).Methods("GET")
    api.HandleFunc("/blocks", e.handleListBlocks).Methods("GET")
    api.HandleFunc("/blocks/{id}", e.handleGetBlock).Methods("GET")

    // Serve frontend
    r.PathPrefix("/").Handler(http.FileServer(http.Dir("./explorer/static")))

    return r
}

// handleGetDashboard returns the current dashboard data
func (e *Explorer) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
    dashboard := e.GetDashboard()
    respondJSON(w, http.StatusOK, dashboard)
}

// handleListBlocks returns paginated blocks
func (e *Explorer) handleListBlocks(w http.ResponseWriter, r *http.Request) {
    query := r.URL.Query()
    offset, _ := strconv.Atoi(query.Get("offset"))
    limit, _ := strconv.Atoi(query.Get("limit"))

    if limit == 0 {
        limit = 10 // default limit
    }

    blocks := e.GetBlocks(offset, limit)
    respondJSON(w, http.StatusOK, blocks)
}


// handleListBlocks returns paginated blocks
func (e *Explorer) handleGetBlock(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    id := vars["id"]
    
    log.Debug().
        Str("path", r.URL.Path).
        Str("id", id).
        Interface("vars", vars).
        Msg("Block details request received")
    
    if id == "" {
        log.Warn().Msg("Missing block ID in request")
        respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing block ID"})
        return
    }
    
    // Try to find the block by multiple methods
    block, found, err := e.findBlockByAnyID(id)
    
    if err != nil {
        log.Error().Err(err).Str("block_id", id).Msg("Error finding block")
        respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Database error"})
        return
    }
    
    if !found {
        log.Warn().Str("block_id", id).Msg("Block not found")
        respondJSON(w, http.StatusNotFound, map[string]string{"error": "Block not found"})
        return
    }
    
    respondJSON(w, http.StatusOK, block)
}

/// Add this helper function to search for blocks by various ID formats
func (e *Explorer) findBlockByAnyID(id string) (Block, bool, error) {
    // 1. First check the in-memory cache
    e.mutex.RLock()
    for _, block := range e.blocksCache {
        if block.ID == id || block.Nonce == id {
            e.mutex.RUnlock()
            return block, true, nil
        }
    }
    e.mutex.RUnlock()
    
    // 2. Connect to ImmuDB
    client, err := DB_OPs.New()
    if err != nil {
        return Block{}, false, err
    }
    defer client.Close()
    
    // 3. Try direct lookup by nonce first
    key := fmt.Sprintf("crdt:nonce:%s", id)
    var blockData struct {
        Nonce     string            `json:"nonce"`
        Data      map[string]string `json:"data"`
        Sender    string            `json:"sender"`
        Timestamp int64             `json:"timestamp"`
    }
    
    err = client.ReadJSON(key, &blockData)
    if err == nil {
        // Direct key lookup succeeded
        block := Block{
            ID:        e.hasher.HashBlock(blockData.Nonce, blockData.Sender, blockData.Timestamp),
            Nonce:     blockData.Nonce,
            Timestamp: blockData.Timestamp,
            Sender:    blockData.Sender,
            Data:      blockData.Data,
        }
        return block, true, nil
    }
    
    // 4. If direct lookup fails, scan all blocks
    keys, err := client.GetKeys("crdt:nonce:", config.DefaultScanLimit)
    if err != nil {
        return Block{}, false, err
    }
    
    // Check each block's ID, nonce, and other possible identifiers
    for _, k := range keys {
        var data struct {
            Nonce     string            `json:"nonce"`
            Data      map[string]string `json:"data"`
            Sender    string            `json:"sender"`
            Timestamp int64             `json:"timestamp"`
        }
        
        if err := client.ReadJSON(k, &data); err != nil {
            continue
        }
        
        // Generate block ID and check against multiple formats
        blockID := e.hasher.HashBlock(data.Nonce, data.Sender, data.Timestamp)
        
        // Try matching against different formats
        if blockID == id || data.Nonce == id || 
           k[len("crdt:nonce:"):] == id || // Match against raw key
           strings.Contains(blockID, id) || // Try partial match for short IDs 
           (len(id) > 8 && strings.Contains(id, blockID)) { // Try reverse match
            
            block := Block{
                ID:        blockID,
                Nonce:     data.Nonce,
                Timestamp: data.Timestamp,
                Sender:    data.Sender,
                Data:      data.Data,
            }
            return block, true, nil
        }
    }
    
    // Not found after trying all methods
    return Block{}, false, nil
}
// respondJSON is a helper function to send a JSON response
func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
    response, err := json.Marshal(payload)
    if err != nil {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte(err.Error()))
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    w.Write(response)
}

