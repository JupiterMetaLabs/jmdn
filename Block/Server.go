package Block

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"time"

	BlockCommon "gossipnode/Block/common"
	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/config/settings"
	"gossipnode/metrics"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
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

/* UNUSED
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
*/

func submitRawTransaction(c *gin.Context) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.submitRawTransaction")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
	)

	logger().NamedLogger.Info(spanCtx, "Received submit raw transaction request",
		ion.String("client_ip", c.ClientIP()),
		ion.String("method", c.Request.Method),
		ion.String("path", c.Request.URL.Path),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.submitRawTransaction"))

	var tx config.Transaction

	// 1. Bind and validate request
	if err := c.ShouldBindJSON(&tx); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "bind_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Invalid transaction format",
			err,
			ion.String("client_ip", c.ClientIP()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.submitRawTransaction"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid transaction format: " + err.Error()})
		return
	}

	txHash, err := SubmitRawTransaction(spanCtx, &tx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Transaction validation failed",
			err,
			ion.String("client_ip", c.ClientIP()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.submitRawTransaction"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction validation failed: " + err.Error()})
		return
	}

	span.SetAttributes(attribute.String("tx_hash", txHash))
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Transaction submitted successfully",
		ion.String("tx_hash", txHash),
		ion.String("client_ip", c.ClientIP()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.submitRawTransaction"))

	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"transaction_hash": txHash,
		"message":          "Transaction submitted successfully",
	})
}

// SubmitRawTransaction handles pre-signed raw transactions with security validations
func SubmitRawTransaction(logger_ctx context.Context, tx *config.Transaction) (string, error) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(logger_ctx, "BlockServer.SubmitRawTransaction")
	defer span.End()

	startTime := time.Now().UTC()

	if LocalGRO == nil {
		var err error
		LocalGRO, err = BlockCommon.InitializeGRO(GRO.BlockGRPCServerLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return "", fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}

	logger().NamedLogger.Info(spanCtx, "Processing raw transaction",
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.SubmitRawTransaction"))

	// Transaction hash must be present - it should be provided by the client
	if tx.Hash == (common.Hash{}) {
		span.RecordError(errors.New("hash is required"))
		span.SetAttributes(attribute.String("status", "hash_missing"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return "", errors.New("invalid transaction: hash is required and must be provided by the client")
	}

	// Run security checks (includes hash validation)
	status, err := Security.AllChecks(tx)
	if !status || err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "security_check_failed"), attribute.Bool("security_status", status))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Security checks failed",
			err,
			ion.Bool("security_status", status),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.SubmitRawTransaction"))
		return "", err
	}

	span.SetAttributes(attribute.Bool("security_checks_passed", true))

	// Basic transaction validation
	if tx.Value.Cmp(big.NewInt(0)) == 0 || tx.Value.String() == "" {
		if len(tx.Data) == 0 || tx.Data == nil {
			span.RecordError(errors.New("value is 0/empty AND data is 0/empty"))
			span.SetAttributes(attribute.String("status", "validation_failed"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return "", errors.New("invalid transaction: value is 0/empty AND data is 0/empty")
		}
	}

	// Check that To and From addresses are not the same (not checked in security checks)
	if tx.To == tx.From {
		span.RecordError(errors.New("invalid transaction: To and From address are the same"))
		span.SetAttributes(attribute.String("status", "validation_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return "", errors.New("invalid transaction: To and From address are the same")
	}

	txHash := tx.Hash.Hex()
	span.SetAttributes(
		attribute.String("tx_hash", txHash),
		attribute.String("from", tx.From.Hex()),
		attribute.String("to", tx.To.Hex()),
		attribute.String("value", tx.Value.String()),
		attribute.Int("tx_type", int(tx.Type)),
	)

	logger().NamedLogger.Info(spanCtx, "Transaction validated, submitting to mempool",
		ion.String("tx_hash", txHash),
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.SubmitRawTransaction"))

	// Asynchronously submit to mempool with context
	// Create a link to the current span to preserve the trace chain
	link := ion.LinkFromContext(spanCtx)

	LocalGRO.Go(GRO.SubmitRawTransactionThread, func(_ context.Context) error {
		// Use a fresh context background but link it to the original request
		asyncCtx, asyncSpan := logger().NamedLogger.Tracer("BlockServer").Start(context.Background(), "AsyncSubmitRawTransaction", ion.WithLinks(link))
		defer asyncSpan.End()

		if err := SubmitToMempool(asyncCtx, tx, txHash); err != nil {
			logger().NamedLogger.Error(asyncCtx, "Error submitting raw transaction to mempool",
				err,
				ion.String("tx_hash", txHash),
				ion.String("from", tx.From.Hex()),
				ion.String("to", tx.To.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", FILENAME),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockServer.SubmitRawTransaction"))
		} else {
			logger().NamedLogger.Info(asyncCtx, "Raw transaction successfully submitted to mempool",
				ion.String("tx_hash", txHash),
				ion.String("from", tx.From.Hex()),
				ion.String("to", tx.To.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", FILENAME),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockServer.SubmitRawTransaction"))
		}
		return nil
	})

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))

	// Return success response
	return txHash, nil
}

// Global host variable to store the libp2p host instance for network operations
var globalHost host.Host

// SetHostInstance sets the global host instance for transaction propagation
func SetHostInstance(h host.Host) {
	globalHost = h // Uncommented - host is needed for consensus
}

func Startserver(bindAddr string, port int, h host.Host, chainID int) {
	_ = StartserverWithContext(context.Background(), bindAddr, port, h, chainID)
}

// StartserverWithContext starts the transaction/block API server and shuts it down when ctx is cancelled.
func StartserverWithContext(ctx context.Context, bindAddr string, port int, h host.Host, chainID int) error {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll("logs", 0750); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Configure zerolog for transactions
	txLogger := zerolog.New(os.Stdout).With().
		Timestamp().
		Str("component", "transactions").
		Logger()

	// Set up Gin to use our transaction logger
	gin.DefaultWriter = io.MultiWriter(os.Stdout)
	gin.DefaultErrorWriter = io.MultiWriter(os.Stderr)

	// Configure global logger
	SetLogger(txLogger)

	// Configure metrics for Prometheus
	metrics.DatabaseOperations.WithLabelValues("init", "success").Inc()

	router := gin.Default()
	SetHostInstance(h)
	// expectedChainID is now set globally in main.go at startup,
	// independent of whether BlockGen is active. See main.go after ResolveTokens().

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
		clientIP := c.ClientIP()
		method := c.Request.Method

		// Create span for request
		spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.HTTPRequest")
		span.SetAttributes(
			attribute.String("client_ip", clientIP),
			attribute.String("method", method),
			attribute.String("path", path),
		)

		// Process request
		c.Next()

		// Log after request with structured data
		latency := time.Since(start)
		statusCode := c.Writer.Status()

		span.SetAttributes(
			attribute.Int("status_code", statusCode),
			attribute.Float64("latency_seconds", latency.Seconds()),
		)

		// Count requests for Prometheus
		metrics.DatabaseOperations.WithLabelValues("api_request", fmt.Sprintf("%d", statusCode)).Inc()

		// Log request details with structured format
		logger().NamedLogger.Info(spanCtx, "API Request",
			ion.String("client_ip", clientIP),
			ion.String("method", method),
			ion.String("path", path),
			ion.Int("status", statusCode),
			ion.Float64("latency_seconds", latency.Seconds()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.HTTPRequest"))

		span.End()
	})

	// Initialize Security Components
	secCfg := &settings.Get().Security

	// Rate Limiter
	rl, err := gatekeeper.NewRateLimiter(secCfg, secCfg.IPCacheSize)
	if err != nil {
		logger().NamedLogger.Error(ctx, "Failed to init rate limiter", err)
		return err
	} else {
		// Middleware
		middleware := gatekeeper.NewGinMiddleware(secCfg, rl, logger().NamedLogger)
		// Apply Gatekeeper Middleware
		router.Use(middleware.Middleware(settings.ServiceBlockIngestHTTP))
	}

	// Transaction endpoints
	router.POST("/api/submit-raw-tx", submitRawTransaction)
	router.POST("/api/process-block", processZKBlock)
	router.GET("/api/block/:number", getBlockByNumber)
	router.GET("/api/block/hash/:hash", getBlockByHash)
	router.GET("/api/tx/:hash", getTransactionInfo)
	router.GET("/api/latest-block", getLatestBlock)

	// router.POST("/api/contract/compile", compileContract)
	// router.POST("/api/contract/deploy", deployContract)
	// router.POST("/api/contract/execute", executeContract)

	// Add a health check endpoint
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Start server
	portStr := fmt.Sprintf("%s:%d", bindAddr, port)
	logger().NamedLogger.Info(ctx, "Starting transaction generator API",
		ion.Int("port", port),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.StartserverWithContext"))

	srv := &http.Server{
		Addr:              portStr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// --- TLS CONFIGURATION ---
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger().NamedLogger)
	// Configure TLS if enabled
	tlsConfig, err := tlsLoader.LoadServerTLS(settings.ServiceBlockIngestHTTP)
	if err != nil {
		// FAIL HARD: If TLS is enabled in policy but fails to load, we must not start insecurely.
		logger().NamedLogger.Error(ctx, "Failed to load TLS config for Sequencer", err)
		return fmt.Errorf("failed to load TLS config for Sequencer: %w", err)
	}

	if tlsConfig != nil {
		srv.TLSConfig = tlsConfig
		logger().NamedLogger.Info(ctx, "TLS Enabled for Sequencer API")
	}

	errCh := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			errCh <- srv.ListenAndServeTLS("", "")
		} else {
			errCh <- srv.ListenAndServe()
		}
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
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.processZKBlock")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
	)

	logger().NamedLogger.Info(spanCtx, "Received process ZK block request",
		ion.String("client_ip", c.ClientIP()),
		ion.String("method", c.Request.Method),
		ion.String("path", c.Request.URL.Path),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.processZKBlock"))

	// Parse the block data from the request
	var block config.ZKBlock
	if err := c.ShouldBindJSON(&block); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Invalid block data",
			err,
			ion.String("client_ip", c.ClientIP()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.processZKBlock"))
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid block data: %v", err)})
		return
	}

	span.SetAttributes(
		attribute.Int64("block_number", int64(block.BlockNumber)),
		attribute.String("block_hash", block.BlockHash.Hex()),
		attribute.Int("tx_count", len(block.Transactions)),
		attribute.String("block_status", block.Status),
	)

	// Validate block data
	if len(block.Transactions) == 0 {
		span.RecordError(errors.New("block contains no transactions"))
		span.SetAttributes(attribute.String("status", "validation_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "block contains no transactions"})
		return
	}

	if block.Status != "verified" {
		span.RecordError(fmt.Errorf("block status is %s, not verified", block.Status))
		span.SetAttributes(attribute.String("status", "validation_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "block has not been verified by ZKVM"})
		return
	}

	logger().NamedLogger.Info(spanCtx, "Block validated, starting consensus process",
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int("tx_count", len(block.Transactions)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.processZKBlock"))

	// Create consensus instance and start consensus process
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, globalHost)

	consensusStartTime := time.Now().UTC()
	if err := consensus.Start(&block); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "consensus_failed"))
		consensusDuration := time.Since(consensusStartTime).Seconds()
		span.SetAttributes(attribute.Float64("consensus_duration", consensusDuration))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to start consensus process",
			err,
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Float64("consensus_duration", consensusDuration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.processZKBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to start consensus process: %v", err),
		})
		return
	}

	consensusDuration := time.Since(consensusStartTime).Seconds()
	span.SetAttributes(attribute.Float64("consensus_duration", consensusDuration))

	for _, tx := range block.Transactions {
		LogTransaction(
			tx.Hash.Hex(),
			tx.From.Hex(),
			tx.To.Hex(),
			tx.Value.String(),
			fmt.Sprintf("%d", tx.Type),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Block processed successfully",
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int("tx_count", len(block.Transactions)),
		ion.Float64("consensus_duration", consensusDuration),
		ion.Float64("total_duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.processZKBlock"))

	// Return success
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"message": fmt.Sprintf("Block %d with %d transactions processed and propagated successfully",
			block.BlockNumber, len(block.Transactions)),
		"block_hash":   block.BlockHash.Hex(),
		"block_number": block.BlockNumber,
	})
}

// getBlockByNumber retrieves a block by its number
func getBlockByNumber(c *gin.Context) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.getBlockByNumber")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
	)

	blockNumberStr := c.Param("number")
	blockNumber, err := strconv.ParseUint(blockNumberStr, 10, 64)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Invalid block number",
			err,
			ion.String("block_number_str", blockNumberStr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByNumber"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}

	span.SetAttributes(attribute.Int64("block_number", int64(blockNumber)))

	logger().NamedLogger.Info(spanCtx, "Getting block by number",
		ion.Int64("block_number", int64(blockNumber)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getBlockByNumber"))

	ctx, cancel := context.WithTimeout(spanCtx, 15*time.Second)
	defer cancel()
	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Database connection failed",
			err,
			ion.Int64("block_number", int64(blockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByNumber"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer func() {
		logger().NamedLogger.Info(spanCtx, "Putting database connection back to pool",
			ion.String("database", "MainDB Connection"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByNumber"))
		DB_OPs.PutMainDBConnection(mainDBClient)
	}()

	block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "block_not_found"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Block not found",
			err,
			ion.Int64("block_number", int64(blockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByNumber"))
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
		return
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Block lookup by number successful",
		ion.Int64("block_number", int64(blockNumber)),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getBlockByNumber"))

	c.JSON(http.StatusOK, block)
}

// getBlockByHash retrieves a block by its hash
func getBlockByHash(c *gin.Context) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.getBlockByHash")
	defer span.End()

	startTime := time.Now().UTC()
	blockHash := c.Param("hash")
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
		attribute.String("block_hash", blockHash),
	)

	logger().NamedLogger.Info(spanCtx, "Getting block by hash",
		ion.String("block_hash", blockHash),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getBlockByHash"))

	ctx, cancel := context.WithTimeout(spanCtx, 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Database connection failed",
			err,
			ion.String("block_hash", blockHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByHash"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	block, err := DB_OPs.GetZKBlockByHash(mainDBClient, blockHash)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "block_not_found"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Block not found",
			err,
			ion.String("block_hash", blockHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getBlockByHash"))
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
		return
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Block lookup by hash successful",
		ion.String("block_hash", blockHash),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getBlockByHash"))

	c.JSON(http.StatusOK, block)
}

// getTransactionInfo gets detailed information about a transaction
func getTransactionInfo(c *gin.Context) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.getTransactionInfo")
	defer span.End()

	startTime := time.Now().UTC()
	txHash := c.Param("hash")
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
		attribute.String("tx_hash", txHash),
	)

	logger().NamedLogger.Info(spanCtx, "Getting transaction info",
		ion.String("tx_hash", txHash),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.getTransactionInfo"))

	ctx, cancel := context.WithTimeout(spanCtx, 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Database connection failed",
			err,
			ion.String("tx_hash", txHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.getTransactionInfo"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	block, err := DB_OPs.GetTransactionBlock(mainDBClient, txHash)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "transaction_not_found"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Transaction not found",
			err,
			ion.String("tx_hash", txHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.getTransactionInfo"))
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
		span.RecordError(errors.New("transaction found in block but details missing"))
		span.SetAttributes(attribute.String("status", "transaction_details_missing"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Transaction found in block but details missing",
			fmt.Errorf("transaction found in block but details missing"),
			ion.String("tx_hash", txHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockServer.getTransactionInfo"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction found in block but details missing"})
		return
	}

	span.SetAttributes(
		attribute.Int64("block_number", int64(block.BlockNumber)),
		attribute.String("block_hash", block.BlockHash.Hex()),
	)

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Transaction info retrieved successfully",
		ion.String("tx_hash", txHash),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockServer.getTransactionInfo"))

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
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("BlockServer").Start(c.Request.Context(), "BlockServer.getLatestBlock")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("client_ip", c.ClientIP()),
		attribute.String("method", c.Request.Method),
		attribute.String("path", c.Request.URL.Path),
	)

	logger().NamedLogger.Info(spanCtx, "Getting latest block",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getLatestBlock"))

	ctx, cancel := context.WithTimeout(spanCtx, 15*time.Second)
	defer cancel()

	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Database connection failed",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getLatestBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(mainDBClient)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_latest_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get latest block number",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getLatestBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block: %v", err)})
		return
	}

	if latestBlockNumber == 0 {
		span.SetAttributes(attribute.String("status", "no_blocks"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Info(spanCtx, "No blocks in chain yet",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getLatestBlock"))
		c.JSON(http.StatusOK, gin.H{"message": "no blocks in the chain yet"})
		return
	}

	span.SetAttributes(attribute.Int64("latest_block_number", int64(latestBlockNumber)))

	block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, latestBlockNumber)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "get_block_data_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to get latest block data",
			err,
			ion.Int64("latest_block_number", int64(latestBlockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", FILENAME),
			ion.String("topic", BLOCKTOPIC),
			ion.String("function", "BlockServer.getLatestBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block data: %v", err)})
		return
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "Latest block lookup successful",
		ion.Int64("latest_block_number", int64(latestBlockNumber)),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", FILENAME),
		ion.String("topic", BLOCKTOPIC),
		ion.String("function", "BlockServer.getLatestBlock"))

	c.JSON(http.StatusOK, block)
}
