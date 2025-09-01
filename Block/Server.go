package Block

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/messaging"
	"gossipnode/metrics"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)


type APIAccessTuple struct {
    Address     string   `json:"address"`
    StorageKeys []string `json:"storage_keys"`
}

type TransactionResponse struct {
    LegacyTx  *FullTxn `json:"legacy_tx,omitempty"`
    EIP1559Tx *FullTxn `json:"eip1559_tx,omitempty"`
}

type FullTxn struct {
    Transaction     *config.Transaction `json:"transaction"`
    TransactionHash string           `json:"transaction_hash"`
}

// Global mutex to protect account access
var accountMutex sync.Mutex

// Convert API AccessTuple to Block.AccessList
func toBlockAccessList(apiList []APIAccessTuple) config.AccessList {
    if len(apiList) == 0 {
        return config.AccessList{}
    }
    
    result := make(config.AccessList, len(apiList))
    for i, tuple := range apiList {
        storageKeys := make([]common.Hash, len(tuple.StorageKeys))
        for j, key := range tuple.StorageKeys {
            storageKeys[j] = common.HexToHash(key)
        }
        
        result[i] = config.AccessTuple{
            Address:     common.HexToAddress(tuple.Address),
            StorageKeys: storageKeys,
        }
    }
    return result
}

func submitRawTransaction(c *gin.Context) {
    var tx config.Transaction

    // 1. Bind and validate request
    if err := c.ShouldBindJSON(&tx); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid transaction format: " + err.Error()})
        return
    }

    txHash, err := SubmitRawTransaction(&tx)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction validation failed: " + err.Error()})
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "status":          "success",
        "transaction_hash": txHash,
        "message":         "Transaction submitted successfully",
    })
}

// SubmitRawTransaction handles pre-signed raw transactions with security validations
func SubmitRawTransaction(tx *config.Transaction) (string, error){
    // Debugging
    fmt.Println("Transaction: ", tx)
    fmt.Println("Transaction ChainID:", tx.ChainID)
    
    // Run security checks
    status, err := Security.ThreeChecks(tx)
    if !status || err != nil {
        return "", err
    }
    // Debugging
    fmt.Println("Security Checks: ", status)

    // Basic transaction validation
    if tx.Value.Cmp(big.NewInt(0)) == 0 || tx.Value.String() == "" {
        return "", errors.New("invalid transaction: value is 0 or empty")
    }
    // Debugging
    fmt.Println("Basic Transaction Validation: ", tx.Value)
    

    // Check the To and From addresses
    if tx.To == nil || tx.From == nil {
        return "", errors.New("invalid transaction: missing To or From address")
    }else if tx.To == tx.From {
        return "", errors.New("invalid transaction: To and From address are the same")
    }

    // Make transaction hash
    rawTxBytes, err := json.Marshal(tx)
    if err != nil {
        return "", err
    }
    txHash := crypto.Keccak256Hash(rawTxBytes).Hex()
    // Debugging
    fmt.Println("Transaction Hash: ", txHash)
    // Asynchronously submit to mempool with context
    go func() {
        if err := SubmitToMempool(tx, txHash); err != nil {
            log.Error().Err(err).
                Str("txHash", txHash).
                Str("from", tx.From.String()).
                Str("to", tx.To.String()).
                Msg("Error submitting raw transaction to mempool")
        }else {
            log.Info().
                Str("txHash", txHash).
                Str("from", tx.From.String()).
                Str("to", tx.To.String()).
                Msg("Raw transaction successfully submitted to mempool")
        }
    }()

    // Return success response
    return txHash, nil
}
// Global host variable to store the libp2p host instance for network operations
var globalHost host.Host

// SetHostInstance sets the global host instance for transaction propagation
func SetHostInstance(h host.Host) {
    globalHost = h
}


func Startserver(port int, h host.Host) {
    // Create logs directory if it doesn't exist
    if err := os.MkdirAll("logs", 0755); err != nil {
        log.Fatal().Err(err).Msg("Failed to create logs directory")
    }
    
    // Set up transaction logger
    txLogPath := "logs/transactions.log"
    txLogFile, err := os.OpenFile(txLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        log.Fatal().Err(err).Str("path", txLogPath).Msg("Failed to open transaction log file")
    }
    
    // Configure zerolog for transactions
    txLogger := zerolog.New(txLogFile).With().
        Timestamp().
        Str("component", "transactions").
        Logger()
    
    // Set up Gin to use our transaction logger
    gin.DefaultWriter = io.MultiWriter(txLogFile, os.Stdout)
    gin.DefaultErrorWriter = io.MultiWriter(txLogFile, os.Stderr)
    
    // Configure global logger
    SetLogger(txLogger)
    
    // Configure metrics for Prometheus
    metrics.DatabaseOperations.WithLabelValues("init", "success").Inc()
    
    router := gin.Default()
    SetHostInstance(h)
    
    // Add logging middleware
    router.Use(func(c *gin.Context) {
        // CORS headers
        c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization")
        
        if c.Request.Method == "OPTIONS" {
            c.AbortWithStatus(204)
            return
        }
        
        // Start timing
        start := time.Now()
        path := c.Request.URL.Path
        
        // Process request
        c.Next()
        
        // Log after request with structured data
        latency := time.Since(start)
        statusCode := c.Writer.Status()
        clientIP := c.ClientIP()
        method := c.Request.Method
        
        // Count requests for Prometheus
        metrics.DatabaseOperations.WithLabelValues("api_request", fmt.Sprintf("%d", statusCode)).Inc()
        
        // Log request details with structured format for Loki/Grafana
        txLogger.Info().
            Str("client_ip", clientIP).
            Str("method", method).
            Str("path", path).
            Int("status", statusCode).
            Dur("latency", latency).
            Msg("API Request")
    })
    
    // Transaction endpoints
    router.POST("/api/submit-raw-tx", submitRawTransaction)
    router.POST("/api/process-block", processZKBlock)
    router.GET("/api/block/:number", getBlockByNumber)
    router.GET("/api/block/hash/:hash", getBlockByHash)
    router.GET("/api/tx/:hash", getTransactionInfo)
    router.GET("/api/latest-block", getLatestBlock)

    router.POST("/api/contract/compile", compileContract)
    router.POST("/api/contract/deploy", deployContract)
    router.POST("/api/contract/execute", executeContract)

    
    // Add a health check endpoint
    router.GET("/health", func(c *gin.Context) {
        c.JSON(http.StatusOK, gin.H{"status": "ok"})
    })
    
    // Start server
    portStr := fmt.Sprintf(":%d", port)
    txLogger.Info().Int("port", port).Msg("Starting transaction generator API")
    
    if err := router.Run(portStr); err != nil {
        txLogger.Fatal().Err(err).Str("port", portStr).Msg("Failed to start server")
    }
}

func processZKBlock(c *gin.Context) {
    // Parse the block data from the request
    var block config.ZKBlock
    if err := c.ShouldBindJSON(&block); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid block data: %v", err)})
        return
    }
    
    // Validate block data
    if len(block.Transactions) == 0 {
        c.JSON(http.StatusBadRequest, gin.H{"error": "block contains no transactions"})
        return
    }
    
    if block.Status != "verified" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "block has not been verified by ZKVM"})
        return
    }
    

    if err := messaging.PropagateZKBlock(globalHost, &block); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "error": fmt.Sprintf("failed to process/propagate block: %v", err),
        })
        return
    }

    for _, tx := range block.Transactions {
        LogTransaction(
            tx.Hash,
            tx.From,
            tx.To,
            tx.Value,
            tx.Type,
        )
    }

    txLogger.Info().
        Uint64("block_number", block.BlockNumber).
        Str("block_hash", block.BlockHash.Hex()).
        Int("tx_count", len(block.Transactions)).
        Msg("Block processed")
    
    // Return success
    c.JSON(http.StatusOK, gin.H{
        "status": "success",
        "message": fmt.Sprintf("Block %d with %d transactions processed and propagated successfully", 
            block.BlockNumber, len(block.Transactions)),
        "block_hash": block.BlockHash.Hex(),
        "block_number": block.BlockNumber,
    })
}

// getBlockByNumber retrieves a block by its number
func getBlockByNumber(c *gin.Context) {
    blockNumberStr := c.Param("number")
    blockNumber, err := strconv.ParseUint(blockNumberStr, 10, 64)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
        return
    }
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, blockNumber)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
        return
    }

    txLogger.Info().
    Uint64("block_number", blockNumber).
    Msg("Block lookup by number")

    
    c.JSON(http.StatusOK, block)
}

// getBlockByHash retrieves a block by its hash
func getBlockByHash(c *gin.Context) {
    blockHash := c.Param("hash")
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetZKBlockByHash(mainDBClient, blockHash)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
        return
    }

    txLogger.Info().
    Str("block_hash", blockHash).
    Msg("Block lookup by hash")
    
    c.JSON(http.StatusOK, block)
}

// getTransactionInfo gets detailed information about a transaction
func getTransactionInfo(c *gin.Context) {
    txHash := c.Param("hash")
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetTransactionBlock(mainDBClient, txHash)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("transaction not found: %v", err)})
        return
    }
    
    // Find the specific transaction
    var foundTx *config.ZKBlockTransaction
    for _, tx := range block.Transactions {
        if tx.Hash == txHash {
            foundTx = &tx
            break
        }
    }
    
    if foundTx == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction found in block but details missing"})
        return
    }
    
    // Return transaction with block details
    c.JSON(http.StatusOK, gin.H{
        "transaction": foundTx,
        "block_number": block.BlockNumber,
        "block_hash": block.BlockHash.Hex(),
        "timestamp": block.Timestamp,
        "confirmed": true,
    })
}

// getLatestBlock returns information about the latest block
func getLatestBlock(c *gin.Context) {
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(mainDBClient)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block: %v", err)})
        return
    }
    
    if latestBlockNumber == 0 {
        c.JSON(http.StatusOK, gin.H{"message": "no blocks in the chain yet"})
        return
    }
    
    block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, latestBlockNumber)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block data: %v", err)})
        return
    }

    txLogger.Info().
    Uint64("latest_block", latestBlockNumber).
    Msg("Latest block lookup")
    
    c.JSON(http.StatusOK, block)
}