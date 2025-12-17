package Block

import (
	"context"
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

	BlockCommon "gossipnode/Block/common"
	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/logging"

	// "gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/metrics"

	// "gossipnode/PubSubMessages"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
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
	TransactionHash string              `json:"transaction_hash"`
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
		"status":           "success",
		"transaction_hash": txHash,
		"message":          "Transaction submitted successfully",
	})
}

// SubmitRawTransaction handles pre-signed raw transactions with security validations
func SubmitRawTransaction(tx *config.Transaction) (string, error) {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = BlockCommon.InitializeGRO(GRO.BlockGRPCServerLocal)
		if err != nil {
			return "", fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	// Debugging
	fmt.Println("[DEBUG] Transaction: ", tx)

	// Transaction hash must be present - it should be provided by the client
	if tx.Hash == (common.Hash{}) {
		return "", errors.New("invalid transaction: hash is required and must be provided by the client")
	}

	// Run security checks (includes hash validation)
	status, err := Security.AllChecks(tx)
	if !status || err != nil {
		return "", err
	}
	// Debugging
	fmt.Println("[DEBUG] Security Checks: ", status)

	// Basic transaction validation
	if tx.Value.Cmp(big.NewInt(0)) == 0 || tx.Value.String() == "" {
		if len(tx.Data) == 0 || tx.Data == nil {
			return "", errors.New("invalid transaction: value is 0/empty AND data is 0/empty")
		}
	}
	// Debugging
	fmt.Println("[DEBUG] Basic Transaction Validation: ", tx.Value)

	// Check that To and From addresses are not the same (not checked in security checks)
	if tx.To == tx.From {
		return "", errors.New("invalid transaction: To and From address are the same")
	}

	txHash := tx.Hash.Hex()
	// Debugging
	fmt.Println("Transaction Hash: ", txHash)

	// Asynchronously submit to mempool with context
	LocalGRO.Go(GRO.SubmitRawTransactionThread, func(ctx context.Context) error {
		if err := SubmitToMempool(tx, txHash); err != nil {
			log.Error().Err(err).
				Str("txHash", txHash).
				Str("from", tx.From.String()).
				Str("to", tx.To.String()).
				Msg("Error submitting raw transaction to mempool")
		} else {
			log.Info().
				Str("txHash", txHash).
				Str("from", tx.From.String()).
				Str("to", tx.To.String()).
				Msg("Raw transaction successfully submitted to mempool")
		}
		return nil
	})

	// Return success response
	return txHash, nil
}

// Global host variable to store the libp2p host instance for network operations
var globalHost host.Host

// Global chainID variable to store the chain ID for smart contract operations
var globalChainID int

// SetHostInstance sets the global host instance for transaction propagation
func SetHostInstance(h host.Host) {
	globalHost = h // Uncommented - host is needed for consensus
}

func Startserver(port int, h host.Host, chainID int) {
	_ = StartserverWithContext(context.Background(), port, h, chainID)
}

// StartserverWithContext starts the transaction/block API server and shuts it down when ctx is cancelled.
func StartserverWithContext(ctx context.Context, port int, h host.Host, chainID int) error {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll("logs", 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Set up transaction logger
	txLogPath := "logs/transactions.log"
	txLogFile, err := os.OpenFile(txLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open transaction log file %s: %w", txLogPath, err)
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
	globalChainID = chainID
	// Also set expected chain ID in Security module for tx validation
	Security.SetExpectedChainIDBig(big.NewInt(int64(chainID)))
	fmt.Printf("Expected Chain ID: %d - Type: %T\n", chainID, chainID)

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
		start := time.Now().UTC()
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

	srv := &http.Server{
		Addr:              portStr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
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

	// Create consensus instance and start consensus process
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, globalHost)
	// Debugging
	fmt.Printf("Consensus: %+v\n", consensus)
	if err := consensus.Start(&block); err != nil {
		fmt.Printf("Error starting consensus process: %+v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to start consensus process: %v", err),
		})
		return
	}

	for _, tx := range block.Transactions {
		LogTransaction(
			tx.Hash.Hex(),
			tx.From.Hex(),
			tx.To.Hex(),
			tx.Value.String(),
			fmt.Sprintf("%d", tx.Type),
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
		"block_hash":   block.BlockHash.Hex(),
		"block_number": block.BlockNumber,
	})
}

func processZKBlockNoConsensus(c *gin.Context) {
	fmt.Println("=== DEBUG: processZKBlock API called ===")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Parse the block data from the request
	var block config.ZKBlock
	if err := c.ShouldBindJSON(&block); err != nil {
		fmt.Printf("DEBUG: Failed to parse block data: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid block data: %v", err)})
		return
	}
	fmt.Printf("DEBUG: Successfully parsed block data - Block #%d, Hash: %s, Txns: %d\n",
		block.BlockNumber, block.BlockHash.Hex(), len(block.Transactions))

	// Validate block data
	if len(block.Transactions) == 0 {
		fmt.Println("DEBUG: Block contains no transactions")
		c.JSON(http.StatusBadRequest, gin.H{"error": "block contains no transactions"})
		return
	}

	if block.Status != "verified" {
		fmt.Printf("DEBUG: Block status is '%s', not 'verified'\n", block.Status)
		c.JSON(http.StatusBadRequest, gin.H{"error": "block has not been verified by ZKVM"})
		return
	}
	fmt.Println("DEBUG: Block validation passed")

	// Skip consensus for testing - directly process the block
	txLogger.Info().
		Uint64("block_number", block.BlockNumber).
		Str("block_hash", block.BlockHash.Hex()).
		Int("tx_count", len(block.Transactions)).
		Msg("Processing block directly (consensus bypassed for testing)")

	fmt.Println("DEBUG: Getting database connections...")
	// Create DB clients for processing
	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		fmt.Printf("DEBUG: Failed to get main DB connection: %v\n", err)
		txLogger.Error().Err(err).Msg("Failed to get main DB connection")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get main DB connection"})
		return
	}
	fmt.Println("DEBUG: Successfully got main DB connection")
	defer func() {
		fmt.Println("DEBUG: Returning main DB connection to pool")
		DB_OPs.PutMainDBConnection(mainDBClient)
	}()

	accountsClient, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		fmt.Printf("DEBUG: Failed to get accounts DB connection: %v\n", err)
		txLogger.Error().Err(err).Msg("Failed to get accounts DB connection")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get accounts DB connection"})
		return
	}
	fmt.Println("DEBUG: Successfully got accounts DB connection")
	defer func() {
		fmt.Println("DEBUG: Returning accounts DB connection to pool")
		DB_OPs.PutAccountsConnection(accountsClient)
	}()

	fmt.Println("DEBUG: Starting transaction processing...")
	// Process all transactions in the block atomically with rollback capability
	if err := BlockProcessing.ProcessBlockTransactions(&block, accountsClient); err != nil {
		fmt.Printf("DEBUG: Block processing failed: %v\n", err)
		txLogger.Error().
			Err(err).
			Str("block_hash", block.BlockHash.Hex()).
			Msg("Block processing failed")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to process block transactions: %v", err),
		})
		return
	}
	fmt.Println("DEBUG: All transactions processed successfully")

	txLogger.Info().
		Str("block_hash", block.BlockHash.Hex()).
		Msg("All transactions processed successfully - storing block")

	fmt.Println("DEBUG: Storing block in main DB...")
	// Store the validated and processed block in main DB
	if err := DB_OPs.StoreZKBlock(mainDBClient, &block); err != nil {
		fmt.Printf("DEBUG: Failed to store block: %v\n", err)
		txLogger.Error().
			Err(err).
			Str("block_hash", block.BlockHash.Hex()).
			Msg("Failed to store block in database")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to store block: %v", err),
		})
		return
	}
	fmt.Println("DEBUG: Block stored successfully")

	// Log transactions
	fmt.Println("DEBUG: Logging transaction details...")
	for i, tx := range block.Transactions {
		fmt.Printf("DEBUG: Transaction %d - Hash: %s, From: %s, To: %s, Value: %s\n",
			i+1, tx.Hash.Hex(), tx.From.Hex(), tx.To.Hex(), tx.Value.String())
		LogTransaction(
			tx.Hash.Hex(),
			tx.From.Hex(),
			tx.To.Hex(),
			tx.Value.String(),
			fmt.Sprintf("%d", tx.Type),
		)
	}

	txLogger.Info().
		Uint64("block_number", block.BlockNumber).
		Str("block_hash", block.BlockHash.Hex()).
		Int("tx_count", len(block.Transactions)).
		Msg("Block processed and stored successfully")

	fmt.Println("DEBUG: Returning success response")
	// Return success
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"message": fmt.Sprintf("Block %d with %d transactions processed and stored successfully (consensus bypassed)",
			block.BlockNumber, len(block.Transactions)),
		"block_hash":   block.BlockHash.Hex(),
		"block_number": block.BlockNumber,
	})
	fmt.Println("=== DEBUG: processZKBlock API completed successfully ===")
}

// getBlockByNumber retrieves a block by its number
func getBlockByNumber(c *gin.Context) {
	blockNumberStr := c.Param("number")
	blockNumber, err := strconv.ParseUint(blockNumberStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer func() {
		mainDBClient.Client.Logger.Logger.Info("Putting database connection back to pool",
			zap.String(logging.Connection_database, "MainDB Connection"),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, FILENAME),
			zap.String(logging.Topic, BLOCKTOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Block.getBlockByNumber"),
		)
		DB_OPs.PutMainDBConnection(mainDBClient)
	}()

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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	block, err := DB_OPs.GetTransactionBlock(mainDBClient, txHash)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("transaction not found: %v", err)})
		return
	}

	// Find the specific transaction
	var foundTx *config.Transaction
	for _, tx := range block.Transactions {
		if tx.Hash == common.HexToHash(txHash) {
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
		"transaction":  foundTx,
		"block_number": block.BlockNumber,
		"block_hash":   block.BlockHash.Hex(),
		"timestamp":    block.Timestamp,
		"confirmed":    true,
	})
}

// getLatestBlock returns information about the latest block
func getLatestBlock(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

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
