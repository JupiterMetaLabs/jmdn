package explorer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	help "gossipnode/helper"
)

// WebSocket upgrader with CORS support
var upgrader = websocket.Upgrader{
    ReadBufferSize:  1024,
    WriteBufferSize: 1024,
    CheckOrigin: func(r *http.Request) bool {
        // Allow any origin for development; in production, check the origin
        return true
    },
}

// Block represents a block or transaction in the blockchain explorer
type Block struct {
    ID              string                 `json:"id"`
    Nonce           string                 `json:"nonce"`
    Timestamp       int64                  `json:"timestamp"`
    Sender          string                 `json:"sender"`
    Data            map[string]string      `json:"data,omitempty"`
    RawData         map[string]interface{} `json:"raw_data,omitempty"`
    Type            string                 `json:"type"`
    TransactionHash string                 `json:"transaction_hash,omitempty"`
    From            string                 `json:"from,omitempty"`
    To              string                 `json:"to,omitempty"`
    Value           string                 `json:"value,omitempty"`
    GasLimit        string                 `json:"gas_limit,omitempty"`
    GasPrice        string                 `json:"gas_price,omitempty"`
    MaxFee          string                 `json:"max_fee,omitempty"`
    MaxPriorityFee  string                 `json:"max_priority_fee,omitempty"`
    ChainID         string                 `json:"chain_id,omitempty"`
    Hops            int                    `json:"hops"`
    TxID            uint64                 `json:"tx_id,omitempty"`
}

// Transaction represents a transaction in the blockchain
type Transaction struct {
    Hash           string                 `json:"hash"`
    From           string                 `json:"from"`
    To             string                 `json:"to"`
    Value          string                 `json:"value"`
    Type           string                 `json:"type"`
    Timestamp      int64                  `json:"timestamp"`
    GasLimit       string                 `json:"gas_limit,omitempty"`
    GasPrice       string                 `json:"gas_price,omitempty"`
    MaxFee         string                 `json:"max_fee,omitempty"`
    MaxPriorityFee string                 `json:"max_priority_fee,omitempty"`
    ChainID        string                 `json:"chain_id,omitempty"`
    Data           string                 `json:"data,omitempty"`
    Nonce          string                 `json:"nonce,omitempty"`
    RawData        map[string]interface{} `json:"raw_data,omitempty"`
}

// DashboardStats represents key metrics for the dashboard
type DashboardStats struct {
    BlockCount      int     `json:"block_count"`
    TransactionCount int     `json:"transaction_count"`
    NodeCount       int     `json:"node_count"`
    TotalAmount     float64 `json:"total_amount"`
    LatestTxID      uint64  `json:"latest_tx_id"`
}

// Dashboard represents the data for the explorer dashboard
type Dashboard struct {
    Stats             DashboardStats `json:"stats"`
    LatestBlocks      []Block        `json:"latest_blocks"`
    LatestTransactions []Transaction `json:"latest_transactions"`
}

// MessageType represents the type of WebSocket message
type MessageType string

const (
    MessageTypeDashboard   MessageType = "dashboard"
    MessageTypeBlock       MessageType = "block"
    MessageTypeTransaction MessageType = "transaction"
    MessageTypeStats       MessageType = "stats"
)

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
    Type MessageType     `json:"type"`
    Data json.RawMessage `json:"data"`
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
    ImmuClient         *config.ImmuClient
    Clients            map[string]*Client
    Register           chan *Client
    Unregister         chan *Client
    Broadcast          chan []byte
    Dashboard          Dashboard
    mutex              sync.RWMutex
    transactionMutex   sync.RWMutex
    LastCheckedTxID    uint64
    UpdateInterval     time.Duration
    hasher             *config.BlockHasher
    processingBlocks   map[string]bool
    blocksCache        []Block
    transactionsCache  []Transaction
    maxBlockCache      int
    maxTransactionCache int
}

// HandleBroadcast implements the helper.BroadcastHandler interface
func (e *Explorer) HandleBroadcast(data []byte) {
    e.Broadcast <- data
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
        ImmuClient:         immuClient,
        Clients:            make(map[string]*Client),
        Register:           make(chan *Client),
        Unregister:         make(chan *Client),
        Broadcast:          make(chan []byte),
        UpdateInterval:     5 * time.Second,
        processingBlocks:   make(map[string]bool),
        blocksCache:        make([]Block, 0, 100),
        transactionsCache:  make([]Transaction, 0, 500),
        maxBlockCache:      100,
        maxTransactionCache: 500,
        hasher:             DB_OPs.NewBlockHasher(),
    }

    // Initialize dashboard data
    err = explorer.updateDashboard()
    if err != nil {
        DB_OPs.Close(immuClient)
        return nil, err
    }

    // Start processing loops
    go explorer.run()
    go explorer.monitorBlocks()

    // Register with message propagation system
    help.SetBroadcastHandler(explorer)

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
                message := WebSocketMessage{
                    Type: MessageTypeDashboard,
                    Data: dashboardJSON,
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

// monitorBlocks periodically checks for new blocks and transactions in ImmuDB
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
    // Get current state from ImmuDB
    state, err := DB_OPs.GetDatabaseState(e.ImmuClient)
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

    message := WebSocketMessage{
        Type: MessageTypeDashboard,
        Data: dashboardJSON,
    }

    messageJSON, err := json.Marshal(message)
    if err != nil {
        return err
    }

    e.Broadcast <- messageJSON

    // Also broadcast updated stats separately
    e.mutex.RLock()
    statsJSON, err := json.Marshal(e.Dashboard.Stats)
    e.mutex.RUnlock()

    if err == nil {
        statsMessage := WebSocketMessage{
            Type: MessageTypeStats,
            Data: statsJSON,
        }
        statsMessageJSON, _ := json.Marshal(statsMessage)
        e.Broadcast <- statsMessageJSON
    }

    return nil
}

// updateDashboard refreshes the dashboard data from ImmuDB
func (e *Explorer) updateDashboard() error {
    e.mutex.Lock()
    defer e.mutex.Unlock()

    // Get stats from ImmuDB
    stats, blocks, transactions, err := e.fetchBlockchainData()
    if err != nil {
        return err
    }

    // Update dashboard
    e.Dashboard = Dashboard{
        Stats:             stats,
        LatestBlocks:      blocks,
        LatestTransactions: transactions,
    }

    // Update last checked transaction ID
    e.LastCheckedTxID = stats.LatestTxID

    return nil
}

// fetchBlockchainData fetches all necessary data from ImmuDB
func (e *Explorer) fetchBlockchainData() (DashboardStats, []Block, []Transaction, error) {
    // Get current ImmuDB state
    state, err := DB_OPs.GetDatabaseState(e.ImmuClient)
    if err != nil {
        return DashboardStats{}, nil, nil, err
    }

    // Get all nonce keys with the CRDT prefix (blocks)
    nonceKeys, err := DB_OPs.GetKeys(e.ImmuClient,"crdt:nonce:", config.DefaultScanLimit)
    if err != nil {
        return DashboardStats{}, nil, nil, err
    }

    // Get all transaction keys
    txKeys, err := DB_OPs.GetKeys(e.ImmuClient,"tx:", config.DefaultScanLimit)
    if err != nil {
        return DashboardStats{}, nil, nil, err
    }

    // Track nodes and total amount
    nodes := make(map[string]bool)
    var totalAmount float64

    // Process blocks
    blocks := make([]Block, 0, len(nonceKeys))
    for _, key := range nonceKeys {
        nonce := key[len("crdt:nonce:"):]
        
        // Skip if already processing
        if e.processingBlocks[nonce] {
            continue
        }

        // Mark as processing
        e.processingBlocks[nonce] = true

        // Read block data 
        var blockMsg config.BlockMessage
        if err := DB_OPs.ReadJSON(e.ImmuClient,key, &blockMsg); err != nil {
            log.Error().Err(err).Str("key", key).Msg("Failed to read block data")
            delete(e.processingBlocks, nonce)
            continue
        }

        // Convert to explorer block format
        block := convertToExplorerBlock(blockMsg)
        
        // Add to blocks array
        blocks = append(blocks, block)

        // Track unique nodes
        nodes[blockMsg.Sender] = true

        // Add to total amount if it's a payment or transaction with value
        if blockMsg.Type == "transaction" && blockMsg.Data != nil {
            if blockMsg.Transaction != nil && blockMsg.Transaction.Value != nil {
                // Try to extract value from structured transaction
                valueStr := blockMsg.Transaction.Value.String()
                if amountFloat, err := strconv.ParseFloat(valueStr, 64); err == nil {
                    totalAmount += amountFloat
                }
            } else if amount, ok := blockMsg.Data["amount"]; ok {
                // Fallback to data map
                if amountFloat, err := strconv.ParseFloat(amount, 64); err == nil {
                    totalAmount += amountFloat
                }
            }
        }

        // Add to cache
        e.addToBlockCache(block)
    }

    // Process transactions
    transactions := make([]Transaction, 0, len(txKeys))
    
    // Process direct transaction entries
    for _, key := range txKeys {
        var txMsg config.BlockMessage
        if err := DB_OPs.ReadJSON(e.ImmuClient,key, &txMsg); err != nil {
            log.Error().Err(err).Str("key", key).Msg("Failed to read transaction data")
            continue
        }

        // Convert to explorer transaction format
        tx := convertToTransaction(txMsg)
        transactions = append(transactions, tx)

        // Add to cache
        e.addToTransactionCache(tx)
    }

    // Also check regular blocks for transaction types to ensure we catch all transactions
    for _, key := range nonceKeys {
        var blockMsg config.BlockMessage
        if err := DB_OPs.ReadJSON(e.ImmuClient, key, &blockMsg); err != nil {
            continue
        }

        if blockMsg.Type == "transaction" {
            // Convert to explorer transaction format
            tx := convertToTransaction(blockMsg)
            
            // Check if this transaction is already in our list (by hash)
            isDuplicate := false
            for _, existingTx := range transactions {
                if existingTx.Hash == tx.Hash {
                    isDuplicate = true
                    break
                }
            }

            if !isDuplicate {
                transactions = append(transactions, tx)
                e.addToTransactionCache(tx)
            }
        }
    }

    // Sort blocks by timestamp (newest first)
    sort.Slice(blocks, func(i, j int) bool {
        return blocks[i].Timestamp > blocks[j].Timestamp
    })

    // Sort transactions by timestamp (newest first)
    sort.Slice(transactions, func(i, j int) bool {
        return transactions[i].Timestamp > transactions[j].Timestamp
    })

    // Limit the number of blocks and transactions for the dashboard
    latestBlocks := blocks
    if len(latestBlocks) > 10 {
        latestBlocks = latestBlocks[:10]
    }

    latestTxs := transactions
    if len(latestTxs) > 10 {
        latestTxs = latestTxs[:10]
    }

    // Create stats
    stats := DashboardStats{
        BlockCount:      len(nonceKeys),
        TransactionCount: len(transactions),
        NodeCount:       len(nodes),
        TotalAmount:     totalAmount,
        LatestTxID:      state.TxId,
    }

    return stats, latestBlocks, latestTxs, nil
}

// convertToExplorerBlock converts a BlockMessage to the explorer Block format
func convertToExplorerBlock(msg config.BlockMessage) Block {
    block := Block{
        ID:        msg.ID,
        Nonce:     msg.Nonce,
        Timestamp: msg.Timestamp,
        Sender:    msg.Sender,
        Data:      msg.Data,
        Type:      msg.Type,
        Hops:      msg.Hops,
    }

    // Set transaction hash if available
    if msg.Data != nil {
        if txHash, ok := msg.Data["transaction_hash"]; ok {
            block.TransactionHash = txHash
        }
    }

    // Extract transaction details if this is a transaction
    if msg.Type == "transaction" && msg.Transaction != nil {
        tx := msg.Transaction
        
        // Set addresses
        block.From = msg.Sender
        if tx.To != nil {
            block.To = tx.To.Hex()
        }
        
        // Set value if available
        if tx.Value != nil {
            block.Value = tx.Value.String()
        }
        
        // Set gas parameters
        if tx.GasLimit != 0 {
            block.GasLimit = string(tx.GasLimit)
        }
        
        if tx.GasPrice != nil {
            block.GasPrice = tx.GasPrice.String()
        }
        
        if tx.MaxFeePerGas != nil {
            block.MaxFee = tx.MaxFeePerGas.String()
        }
        
        if tx.MaxPriorityFeePerGas != nil {
            block.MaxPriorityFee = tx.MaxPriorityFeePerGas.String()
        }
        
        if tx.ChainID != nil {
            block.ChainID = tx.ChainID.String()
        }
    }

    return block
}

// convertToTransaction converts a BlockMessage to the explorer Transaction format
func convertToTransaction(msg config.BlockMessage) Transaction {
    tx := Transaction{
        From:      msg.Sender,
        Timestamp: msg.Timestamp,
        Type:      "unknown",
    }

    // Get transaction hash
    if msg.Data != nil {
        if txHash, ok := msg.Data["transaction_hash"]; ok {
            tx.Hash = txHash
        }
    }

    // Extract transaction details if available
    if msg.Transaction != nil {
        // Set type
        if msg.Transaction.MaxFeePerGas != nil && msg.Transaction.MaxPriorityFeePerGas != nil {
            tx.Type = "eip1559"
        } else if msg.Transaction.GasPrice != nil {
            tx.Type = "legacy"
        }
        
        // Set recipient if available
        if msg.Transaction.To != nil {
            tx.To = msg.Transaction.To.Hex()
        }
        
        // Set value if available
        if msg.Transaction.Value != nil {
            tx.Value = msg.Transaction.Value.String()
        }
        
        // Set gas parameters
        if msg.Transaction.GasLimit != 0 {
            tx.GasLimit = string(msg.Transaction.GasLimit)
        }
        
        if msg.Transaction.GasPrice != nil {
            tx.GasPrice = msg.Transaction.GasPrice.String()
        }
        
        if msg.Transaction.MaxFeePerGas != nil {
            tx.MaxFee = msg.Transaction.MaxFeePerGas.String()
        }
        
        if msg.Transaction.MaxPriorityFeePerGas != nil {
            tx.MaxPriorityFee = msg.Transaction.MaxPriorityFeePerGas.String()
        }
        
        if msg.Transaction.ChainID != nil {
            tx.ChainID = msg.Transaction.ChainID.String()
        }
        
        if msg.Transaction.Nonce != 0 {
            tx.Nonce = string(msg.Transaction.Nonce)
        }
        
        // Set data if available
        if len(msg.Transaction.Data) > 0 {
            tx.Data = string(msg.Transaction.Data)
        }
    } else if msg.Data != nil {
        // Fallback to data fields
        tx.RawData = make(map[string]interface{})
        for k, v := range msg.Data {
            tx.RawData[k] = v
        }
    }

    return tx
}

// addToBlockCache adds a block to the in-memory cache
func (e *Explorer) addToBlockCache(block Block) {
    // Check if already in cache
    for _, b := range e.blocksCache {
        if b.ID == block.ID || (b.Nonce == block.Nonce && b.Timestamp == block.Timestamp) {
            return
        }
    }

    // Add to front of cache
    e.blocksCache = append([]Block{block}, e.blocksCache...)

    // Trim if needed
    if len(e.blocksCache) > e.maxBlockCache {
        e.blocksCache = e.blocksCache[:e.maxBlockCache]
    }
}

// addToTransactionCache adds a transaction to the in-memory cache
func (e *Explorer) addToTransactionCache(tx Transaction) {
    e.transactionMutex.Lock()
    defer e.transactionMutex.Unlock()
    
    // Check if already in cache
    for _, t := range e.transactionsCache {
        if t.Hash == tx.Hash {
            return
        }
    }

    // Add to front of cache
    e.transactionsCache = append([]Transaction{tx}, e.transactionsCache...)

    // Trim if needed
    if len(e.transactionsCache) > e.maxTransactionCache {
        e.transactionsCache = e.transactionsCache[:e.maxTransactionCache]
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

// GetTransactions returns transactions from the cache with pagination
func (e *Explorer) GetTransactions(offset, limit int) []Transaction {
    e.transactionMutex.RLock()
    defer e.transactionMutex.RUnlock()

    if offset >= len(e.transactionsCache) {
        return []Transaction{}
    }

    end := offset + limit
    if end > len(e.transactionsCache) {
        end = len(e.transactionsCache)
    }

    return e.transactionsCache[offset:end]
}

// GetBlockByID returns a specific block by ID
func (e *Explorer) GetBlockByID(id string) (Block, bool) {
    e.mutex.RLock()
    // First check the in-memory cache
    for _, block := range e.blocksCache {
        if block.ID == id || block.Nonce == id {
            e.mutex.RUnlock()
            return block, true
        }
    }
    e.mutex.RUnlock()
    
    // If not found in cache, try direct lookup from ImmuDB
    block, found, err := e.findBlockByAnyID(id)
    if err != nil || !found {
        return Block{}, false
    }
    
    return block, true
}

// GetTransactionByHash returns a specific transaction by hash
func (e *Explorer) GetTransactionByHash(hash string) (Transaction, bool) {
    e.transactionMutex.RLock()
    // First check the in-memory cache
    for _, tx := range e.transactionsCache {
        if tx.Hash == hash {
            e.transactionMutex.RUnlock()
            return tx, true
        }
    }
    e.transactionMutex.RUnlock()
    
    // If not found in cache, try direct lookup from ImmuDB
    tx, found, err := e.findTransactionByHash(hash)
    if err != nil || !found {
        return Transaction{}, false
    }
    
    return tx, true
}

// findBlockByAnyID searches for a block by various ID formats
func (e *Explorer) findBlockByAnyID(id string) (Block, bool, error) {
    // Try direct lookup by nonce first
    key := fmt.Sprintf("crdt:nonce:%s", id)
    var blockMsg config.BlockMessage
    
    err := DB_OPs.ReadJSON(e.ImmuClient,key, &blockMsg)
    if err == nil {
        // Direct key lookup succeeded
        block := convertToExplorerBlock(blockMsg)
        
        // Add to cache for future lookups
        e.mutex.Lock()
        e.addToBlockCache(block)
        e.mutex.Unlock()
        
        return block, true, nil
    }
    
    // If direct lookup fails, scan all blocks
    keys, err := DB_OPs.GetKeys(e.ImmuClient,"crdt:nonce:", config.DefaultScanLimit)
    if err != nil {
        return Block{}, false, err
    }
    
    // Check each block's ID, nonce, and other possible identifiers
    for _, k := range keys {
        var msg config.BlockMessage
        
        if err := DB_OPs.ReadJSON(e.ImmuClient, k, &msg); err != nil {
            continue
        }
        
        // Try matching against different formats
        if msg.ID == id || msg.Nonce == id || 
           (msg.Data != nil && msg.Data["transaction_hash"] == id) || 
           strings.HasSuffix(k, id) || // Match against key suffix 
           strings.Contains(msg.ID, id) || // Try partial match for short IDs
           (len(id) > 8 && strings.Contains(id, msg.ID)) { // Try reverse match
            
            block := convertToExplorerBlock(msg)
            
            // Add to cache for future lookups
            e.mutex.Lock()
            e.addToBlockCache(block)
            e.mutex.Unlock()
            
            return block, true, nil
        }
    }
    
    // Not found after trying all methods
    return Block{}, false, nil
}

// findTransactionByHash searches for a transaction by hash
func (e *Explorer) findTransactionByHash(hash string) (Transaction, bool, error) {
    // Try direct lookup using tx: prefix
    key := fmt.Sprintf("tx:%s", hash)
    var txMsg config.BlockMessage
    
    err := DB_OPs.ReadJSON(e.ImmuClient,key, &txMsg)
    if err == nil {
        // Direct key lookup succeeded
        tx := convertToTransaction(txMsg)
        
        // Add to cache for future lookups
        e.transactionMutex.Lock()
        e.addToTransactionCache(tx)
        e.transactionMutex.Unlock()
        
        return tx, true, nil
    }
    
    // If direct lookup fails, scan all blocks
    keys, err := DB_OPs.GetKeys(e.ImmuClient,"crdt:nonce:", config.DefaultScanLimit)
    if err != nil {
        return Transaction{}, false, err
    }
    
    // Check each block for the transaction hash
    for _, k := range keys {
        var msg config.BlockMessage
        
        if err := DB_OPs.ReadJSON(e.ImmuClient,k, &msg); err != nil {
            continue
        }
        
        // Check if this is a transaction and has the matching hash
        if msg.Type == "transaction" && msg.Data != nil {
            if txHash, ok := msg.Data["transaction_hash"]; ok && txHash == hash {
                tx := convertToTransaction(msg)
                
                // Add to cache for future lookups
                e.transactionMutex.Lock()
                e.addToTransactionCache(tx)
                e.transactionMutex.Unlock()
                
                return tx, true, nil
            }
        }
    }
    
    // Not found after trying all methods
    return Transaction{}, false, nil
}

// GetDashboard returns the current dashboard data
func (e *Explorer) GetDashboard() Dashboard {
    e.mutex.RLock()
    defer e.mutex.RUnlock()
    return e.Dashboard
}

// handleWebSocket handles WebSocket connections
func (e *Explorer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

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

    c.Conn.SetReadLimit(4096)
    c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
    c.Conn.SetPongHandler(func(string) error {
        c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
        return nil
    })

    for {
        _, message, err := c.Conn.ReadMessage()
        if err != nil {
            if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
                log.Error().Err(err).Msg("WebSocket read error")
            }
            break
        }
        
        // Process client messages (queries, etc.)
        log.Debug().Str("client", c.ID).Str("message", string(message)).Msg("Received message from client")
        
        // Handle client queries
        var query struct {
            Type string `json:"type"`
            ID   string `json:"id,omitempty"`
            Hash string `json:"hash,omitempty"`
        }
        
        if err := json.Unmarshal(message, &query); err == nil {
            switch query.Type {
            case "get_block":
                if block, found := c.Explorer.GetBlockByID(query.ID); found {
                    blockJSON, _ := json.Marshal(block)
                    response := WebSocketMessage{
                        Type: MessageTypeBlock,
                        Data: blockJSON,
                    }
                    responseJSON, _ := json.Marshal(response)
                    c.Send <- responseJSON
                }
            case "get_transaction":
                if tx, found := c.Explorer.GetTransactionByHash(query.Hash); found {
                    txJSON, _ := json.Marshal(tx)
                    response := WebSocketMessage{
                        Type: MessageTypeTransaction,
                        Data: txJSON,
                    }
                    responseJSON, _ := json.Marshal(response)
                    c.Send <- responseJSON
                }
            case "refresh_dashboard":
                // Client is requesting a dashboard refresh
                c.Explorer.mutex.RLock()
                dashboardJSON, _ := json.Marshal(c.Explorer.Dashboard)
                c.Explorer.mutex.RUnlock()
                
                response := WebSocketMessage{
                    Type: MessageTypeDashboard,
                    Data: dashboardJSON,
                }
                responseJSON, _ := json.Marshal(response)
                c.Send <- responseJSON
            }
        }
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
    
    // Dashboard data
    api.HandleFunc("/dashboard", e.handleGetDashboard).Methods("GET")
    
    // Block endpoints
    api.HandleFunc("/blocks", e.handleListBlocks).Methods("GET")
    api.HandleFunc("/blocks/{id}", e.handleGetBlock).Methods("GET")
    
    // Transaction endpoints
    api.HandleFunc("/transactions", e.handleListTransactions).Methods("GET")
    api.HandleFunc("/transactions/{hash}", e.handleGetTransaction).Methods("GET")
    
    // Statistics endpoint
    api.HandleFunc("/stats", e.handleGetStats).Methods("GET")
    
    // Health check
    api.HandleFunc("/health", e.handleHealthCheck).Methods("GET")

    // Serve frontend
    r.PathPrefix("/js/").Handler(http.StripPrefix("/js/", http.FileServer(http.Dir("./explorer/static/js"))))
    r.PathPrefix("/css/").Handler(http.StripPrefix("/css/", http.FileServer(http.Dir("./explorer/static/css"))))
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
    
    respondJSON(w, http.StatusOK, helper.Map(map[string]interface{}{
        "blocks": blocks,
        "count":  len(blocks),
        "offset": offset,
        "limit":  limit,
        "total":  len(e.blocksCache),
    }))
}

// handleGetBlock returns a specific block
func (e *Explorer) handleGetBlock(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    id := vars["id"]
    
    log.Debug().
        Str("path", r.URL.Path).
        Str("id", id).
        Msg("Block details request received")
    
    if id == "" {
        log.Warn().Msg("Missing block ID in request")
        respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing block ID"})
        return
    }
    
    block, found := e.GetBlockByID(id)
    
    if !found {
        log.Warn().Str("block_id", id).Msg("Block not found")
        respondJSON(w, http.StatusNotFound, map[string]string{"error": "Block not found"})
        return
    }
    
    respondJSON(w, http.StatusOK, block)
}

// handleListTransactions returns paginated transactions
func (e *Explorer) handleListTransactions(w http.ResponseWriter, r *http.Request) {
    query := r.URL.Query()
    offset, _ := strconv.Atoi(query.Get("offset"))
    limit, _ := strconv.Atoi(query.Get("limit"))
    
    if limit == 0 {
        limit = 10 // default limit
    }

    transactions := e.GetTransactions(offset, limit)
    
    respondJSON(w, http.StatusOK, helper.Map(map[string]interface{}{
        "transactions": transactions,
        "count":        len(transactions),
        "offset":       offset,
        "limit":        limit,
        "total":        len(e.transactionsCache),
    }))
}

// handleGetTransaction returns a specific transaction
func (e *Explorer) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    hash := vars["hash"]
    
    log.Debug().
        Str("path", r.URL.Path).
        Str("hash", hash).
        Msg("Transaction details request received")
    
    if hash == "" {
        log.Warn().Msg("Missing transaction hash in request")
        respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing transaction hash"})
        return
    }
    
    tx, found := e.GetTransactionByHash(hash)
    
    if !found {
        log.Warn().Str("hash", hash).Msg("Transaction not found")
        respondJSON(w, http.StatusNotFound, map[string]string{"error": "Transaction not found"})
        return
    }
    
    respondJSON(w, http.StatusOK, tx)
}

// handleGetStats returns current blockchain statistics
func (e *Explorer) handleGetStats(w http.ResponseWriter, r *http.Request) {
    e.mutex.RLock()
    stats := e.Dashboard.Stats
    e.mutex.RUnlock()
    
    respondJSON(w, http.StatusOK, stats)
}

// handleHealthCheck returns the health status of the explorer
func (e *Explorer) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
    // Check ImmuDB client
    isHealthy := true
    
    // Get database state as a health check
    _, err := DB_OPs.GetDatabaseState(e.ImmuClient)
    if err != nil {
        isHealthy = false
    }
    
    status := "ok"
    if !isHealthy {
        status = "error"
    }
    
    respondJSON(w, http.StatusOK, helper.Map(map[string]interface{}{
        "status":    status,
        "service":   "blockchain-explorer",
        "timestamp": time.Now().UTC(),
    }))
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

// gin is just for the H type
type mapHelper struct{}

func (h mapHelper) Map(data map[string]interface{}) map[string]interface{} {
    return data
}

var helper = &mapHelper{}