package explorer

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/thebestatus"
	"gossipnode/config"
	"gossipnode/config/GRO"

	explorer_common "gossipnode/explorer/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
)

var BlockOpsLocalGRO interfaces.LocalGoroutineManagerInterface

type stats struct {
	DBState           *thebestatus.Status
	MerkleRoot        string
	LatestBlockNumber uint64
	TotalBlocks       uint64
	TotalDIDs         int64
	TotalTransactions int64
	TotalAddresses    int64
}

type LatestBlockStats struct {
	BlockNumber uint64
	BlockHash   string
	StateRoot   string
	Timestamp   int64
}

// Get block by number
func (s *ImmuDBServer) getBlockByNumber(c *gin.Context) {
	// Create span for getBlockByNumber
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getBlockByNumber")
	defer span.End()

	startTime := time.Now().UTC()
	number := c.Param("number")
	span.SetAttributes(attribute.String("block_number", number))

	numberInt, err := ConvertStringToUint64(number)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "bad_request"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}

	block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, numberInt)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get block by number",
			err,
			ion.Uint64("block_number", numberInt),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getBlockByNumber"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.String("block_hash", block.BlockHash.Hex()),
		attribute.Int64("block_number", int64(block.BlockNumber)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Block retrieved by number",
		ion.Uint64("block_number", numberInt),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getBlockByNumber"))

	c.JSON(http.StatusOK, block)
}

// Get block by hash
func (s *ImmuDBServer) getBlock(c *gin.Context) {
	// Create span for getBlock
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getBlock")
	defer span.End()

	startTime := time.Now().UTC()
	hash := c.Param("id")
	span.SetAttributes(attribute.String("block_hash", hash))

	block, err := DB_OPs.GetZKBlockByHash(&s.defaultdb, hash)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get block by hash",
			err,
			ion.String("block_hash", hash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int64("block_number", int64(block.BlockNumber)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Block retrieved by hash",
		ion.String("block_hash", hash),
		ion.Uint64("block_number", block.BlockNumber),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getBlock"))

	c.JSON(http.StatusOK, block)
}

// List all blocks by pagination
func (s *ImmuDBServer) listBlocks(c *gin.Context) {
	// Create span for listBlocks
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.listBlocks")
	defer span.End()

	startTime := time.Now().UTC()

	// Get pagination parameters with defaults
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	// Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10 // Default to 10 items per page, max 100
	}

	span.SetAttributes(
		attribute.Int("page", page),
		attribute.Int("limit", limit),
	)

	// Get total number of blocks for pagination metadata
	totalBlocks, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get total blocks count",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.listBlocks"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get total blocks count"})
		return
	}

	// Calculate offset and limit for pagination
	offset := (page - 1) * limit
	endBlock := totalBlocks - uint64(offset)
	var startBlock uint64
	if endBlock > uint64(limit) {
		startBlock = endBlock - uint64(limit) + 1
	} else {
		startBlock = 1
	}

	// Get blocks for the current page
	var blocks []*config.ZKBlock
	for i := endBlock; i >= startBlock; i-- {
		block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, i)
		if err != nil {
			// If a block is not found, it might be a gap in the blockchain.
			// Log the error and continue to the next block to provide a partial response.
			s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get block, skipping",
				err,
				ion.Uint64("block_number", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.listBlocks"))
			continue
		}
		blocks = append(blocks, block)
	}

	// Calculate pagination metadata
	totalPages := (int(totalBlocks) + limit - 1) / limit
	hasNext := page < totalPages
	hasPrev := page > 1

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int("blocks_returned", len(blocks)),
		attribute.Int64("total_blocks", int64(totalBlocks)),
		attribute.Int("total_pages", totalPages),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Blocks listed successfully",
		ion.Int("page", page),
		ion.Int("limit", limit),
		ion.Int("blocks_returned", len(blocks)),
		ion.Uint64("total_blocks", totalBlocks),
		ion.Int("total_pages", totalPages),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.listBlocks"))

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"data": blocks,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  totalPages,
			"total_items":  totalBlocks,
			"has_next":     hasNext,
			"has_prev":     hasPrev,
		},
	})
}

/* UNUSED
func (s *ImmuDBServer) getTransactionBlock(c *gin.Context) {
	// Create span for getTransactionBlock
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getTransactionBlock")
	defer span.End()

	startTime := time.Now().UTC()
	hash := c.Param("hash")
	span.SetAttributes(attribute.String("transaction_hash", hash))

	block, err := DB_OPs.GetTransactionBlock(&s.defaultdb, hash)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get transaction block",
			err,
			ion.String("transaction_hash", hash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getTransactionBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int64("block_number", int64(block.BlockNumber)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	c.JSON(http.StatusOK, block)
}
*/

func (s *ImmuDBServer) getLatestBlock(c *gin.Context) {
	// Create span for getLatestBlock
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getLatestBlock")
	defer span.End()

	startTime := time.Now().UTC()

	// Get the latest block number
	latestBlockNumber, err := GetLatesBlockNumber(s)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get latest block number",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getLatestBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get the latest block by number
	block, err := GetLatestBlockByNumber(s, latestBlockNumber)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get latest block",
			err,
			ion.Uint64("latest_block_number", latestBlockNumber),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getLatestBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int64("block_number", int64(block.BlockNumber)),
		attribute.String("block_hash", block.BlockHash.Hex()),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Latest block retrieved",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getLatestBlock"))

	c.JSON(http.StatusOK, block)
}

func (s *ImmuDBServer) getLatestBlockStats(c *gin.Context) {
	// Create span for getLatestBlockStats
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getLatestBlockStats")
	defer span.End()

	startTime := time.Now().UTC()

	latestBlockNumber, err := GetLatesBlockNumber(s)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	block, err := GetLatestBlockByNumber(s, latestBlockNumber)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get the latest block stats with block number, block hash, state root, timestamp
	latestBlockStats := LatestBlockStats{
		BlockNumber: block.BlockNumber,
		BlockHash:   block.BlockHash.Hex(),
		StateRoot:   block.StateRoot.Hex(),
		Timestamp:   block.Timestamp,
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int64("block_number", int64(latestBlockStats.BlockNumber)),
		attribute.String("block_hash", latestBlockStats.BlockHash),
		attribute.String("state_root", latestBlockStats.StateRoot),
		attribute.Int64("timestamp", latestBlockStats.Timestamp),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Latest block stats retrieved",
		ion.Uint64("block_number", latestBlockStats.BlockNumber),
		ion.String("block_hash", latestBlockStats.BlockHash),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getLatestBlockStats"))

	c.JSON(http.StatusOK, latestBlockStats)
}

// Get transaction by hash
func (s *ImmuDBServer) getTransaction(c *gin.Context) {
	// Create span for getTransaction
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getTransaction")
	defer span.End()

	startTime := time.Now().UTC()
	hash := c.Param("hash")
	span.SetAttributes(attribute.String("transaction_hash", hash))

	transaction, err := DB_OPs.GetTransactionByHash(&s.defaultdb, hash)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get transaction by hash",
			err,
			ion.String("transaction_hash", hash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getTransaction"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(attribute.String("status", "success"))
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Transaction retrieved by hash",
		ion.String("transaction_hash", hash),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getTransaction"))

	c.JSON(http.StatusOK, transaction)
}

// Get list of transactions in a given block
func (s *ImmuDBServer) listTransactions_inBlock(c *gin.Context) {
	// Create span for listTransactions_inBlock
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.listTransactions_inBlock")
	defer span.End()

	startTime := time.Now().UTC()
	blockNumber := c.Param("number")
	span.SetAttributes(attribute.String("block_number", blockNumber))

	blockNumberInt, err := ConvertStringToUint64(blockNumber)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "bad_request"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}

	BlockData, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, blockNumberInt)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get block by number",
			err,
			ion.Uint64("block_number", blockNumberInt),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.listTransactions_inBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	Transactions := make([]*config.Transaction, 0)
	for _, tx := range BlockData.Transactions {
		Transactions = append(Transactions, &tx)
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int("transactions_count", len(Transactions)),
		attribute.Int64("block_number", int64(blockNumberInt)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Transactions in block retrieved",
		ion.Uint64("block_number", blockNumberInt),
		ion.Int("transactions_count", len(Transactions)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.listTransactions_inBlock"))

	c.JSON(http.StatusOK, Transactions)
}

// Get default db stats
func (s *ImmuDBServer) getStats(c *gin.Context) {
	// Create span for getStats
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getStats")
	defer span.End()

	startTime := time.Now().UTC()

	if BlockOpsLocalGRO == nil {
		var err error
		BlockOpsLocalGRO, err = explorer_common.InitializeGRO(GRO.ExplorerBlockOpsLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "error"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Errorf("failed to initialize local gro: %v", err)})
			return
		}
	}
	// Create a context with 60 second timeout
	ctxWithTimeout, cancel := context.WithTimeout(spanCtx, 60*time.Second)
	defer cancel()

	var stats stats
	wg, err := BlockOpsLocalGRO.NewFunctionWaitGroup(ctxWithTimeout, GRO.ExplorerBlockOpsWaitGroup)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Errorf("failed to create function wait group: %w", err)})
		return
	}
	var mu sync.Mutex

	// Channel to collect errors from goroutines
	errChan := make(chan error, 6)

	// Function to handle errors from goroutines
	handleErr := func(err error) {
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
		}
	}

	// Get database status in a goroutine
	BlockOpsLocalGRO.Go(GRO.ExplorerBlockOpsThread, func(ctx context.Context) error {
		status, err := thebestatus.StatusFromCurrentDB(ctx)
		if err != nil {
			handleErr(fmt.Errorf("failed to get database state: %w", err))
			return nil
		}
		mu.Lock()
		stats.DBState = &status
		mu.Unlock()
		return nil
	}, local.AddToWaitGroup(GRO.ExplorerBlockOpsWaitGroup))

	// Get merkle root in a goroutine
	BlockOpsLocalGRO.Go(GRO.ExplorerBlockOpsThread, func(ctx context.Context) error {
		merkleroot, err := DB_OPs.GetMerkleRoot(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to get merkle root: %w", err))
			return fmt.Errorf("failed to get merkle root: %w", err)
		}
		mu.Lock()
		stats.MerkleRoot = hex.EncodeToString(merkleroot)
		mu.Unlock()
		return nil
	}, local.AddToWaitGroup(GRO.ExplorerBlockOpsWaitGroup))

	// Get latest block number and total blocks in a goroutine
	BlockOpsLocalGRO.Go(GRO.ExplorerBlockOpsThread, func(ctx context.Context) error {
		latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to get latest block number: %w", err))
			return fmt.Errorf("failed to get latest block number: %w", err)
		}
		mu.Lock()
		stats.LatestBlockNumber = latestBlockNumber
		stats.TotalBlocks = latestBlockNumber + 1
		mu.Unlock()

		// Get total transactions count efficiently
		totalTx, err := DB_OPs.CountTransactions(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to count transactions: %w", err))
			return fmt.Errorf("failed to count transactions: %w", err)
		}
		mu.Lock()
		stats.TotalTransactions = int64(totalTx)
		mu.Unlock()
		return nil
	}, local.AddToWaitGroup(GRO.ExplorerBlockOpsWaitGroup))

	// Get total DIDs in a goroutine
	BlockOpsLocalGRO.Go(GRO.ExplorerBlockOpsThread, func(ctx context.Context) error {
		totalDIDs, err := DB_OPs.CountAccounts(&s.accountsdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to list DIDs: %w", err))
			return fmt.Errorf("failed to list DIDs: %w", err)
		}
		mu.Lock()
		stats.TotalDIDs = int64(totalDIDs)
		mu.Unlock()
		return nil
	}, local.AddToWaitGroup(GRO.ExplorerBlockOpsWaitGroup))

	// Wait for completion OR timeout. Avoid writing responses from goroutines.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All work finished; safe to close and drain errChan.
		close(errChan)
	case <-ctxWithTimeout.Done():
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": ctxWithTimeout.Err().Error()})
		return
	}

	// Check for any errors
	for err := range errChan {
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "error"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			s.defaultdb.Client.Logger.Error(spanCtx, "Error collecting stats",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.getStats"))
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int64("latest_block_number", int64(stats.LatestBlockNumber)),
		attribute.Int64("total_blocks", int64(stats.TotalBlocks)),
		attribute.Int64("total_transactions", stats.TotalTransactions),
		attribute.Int64("total_dids", stats.TotalDIDs),
		attribute.String("merkle_root", stats.MerkleRoot),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Stats retrieved successfully",
		ion.Uint64("latest_block_number", stats.LatestBlockNumber),
		ion.Uint64("total_blocks", stats.TotalBlocks),
		ion.Int64("total_transactions", stats.TotalTransactions),
		ion.Int64("total_dids", stats.TotalDIDs),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getStats"))

	c.JSON(http.StatusOK, stats)
}

func (s *ImmuDBServer) getDIDDetailsFromAddr(c *gin.Context) {
	// Create span for getDIDDetailsFromAddr
	spanCtx, span := s.accountsdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getDIDDetailsFromAddr")
	defer span.End()

	startTime := time.Now().UTC()

	// Change from c.Param to c.Query since addr is a query parameter
	addr := c.Query("addr")
	span.SetAttributes(attribute.String("address", addr))

	if addr == "" {
		span.SetAttributes(attribute.String("status", "bad_request"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "addr query parameter is required"})
		return
	}

	DIDDocument, err := DB_OPs.GetAccount(&s.accountsdb, common.HexToAddress(addr))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.accountsdb.Client.Logger.Error(spanCtx, "Failed to get DID details from address",
			err,
			ion.String("address", addr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getDIDDetailsFromAddr"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.String("did", DIDDocument.DIDAddress),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.accountsdb.Client.Logger.Info(spanCtx, "DID details retrieved from address",
		ion.String("address", addr),
		ion.String("did", DIDDocument.DIDAddress),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getDIDDetailsFromAddr"))

	c.JSON(http.StatusOK, DIDDocument)
}

// Get the Missing blocks
func (s *ImmuDBServer) getMissingBlocks(c *gin.Context) {
	// Create span for getMissingBlocks
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getMissingBlocks")
	defer span.End()

	startTime := time.Now().UTC()
	number := c.Param("number")
	span.SetAttributes(attribute.String("start_block_number", number))

	blockNumberInt, err := ConvertStringToUint64(number)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "bad_request"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}

	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get latest block number",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.getMissingBlocks"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if blockNumberInt > latestBlockNumber {
		span.SetAttributes(attribute.String("status", "bad_request"), attribute.String("reason", "block_number_too_high"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "block number is greater than latest block number"})
		return
	}

	var missingBlocks []*config.ZKBlock
	for blockNumberInt < latestBlockNumber {
		blockNumberInt++
		block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, blockNumberInt)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "error"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get missing block",
				err,
				ion.Uint64("block_number", blockNumberInt),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.getMissingBlocks"))
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		missingBlocks = append(missingBlocks, block)
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int("missing_blocks_count", len(missingBlocks)),
		attribute.Int64("latest_block_number", int64(latestBlockNumber)),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Missing blocks retrieved",
		ion.Int("missing_blocks_count", len(missingBlocks)),
		ion.Uint64("latest_block_number", latestBlockNumber),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getMissingBlocks"))

	c.JSON(http.StatusOK, missingBlocks)
}

func (s *ImmuDBServer) listTransactions(c *gin.Context) {
	// Create span for listTransactions
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.listTransactions")
	defer span.End()

	startTime := time.Now().UTC()

	// Get pagination parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10 // Default to 10 items per page, max 100
	}

	span.SetAttributes(
		attribute.Int("page", page),
		attribute.Int("limit", limit),
	)

	// Calculate offset
	offset := (page - 1) * limit

	// Get paginated transactions directly from database (database-level pagination)
	transactions, total, err := DB_OPs.GetTransactionsPaginated(&s.defaultdb, offset, limit)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to fetch transactions",
			err,
			ion.Int("page", page),
			ion.Int("limit", limit),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.listTransactions"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions: " + err.Error()})
		return
	}

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int("transactions_returned", len(transactions)),
		attribute.Int64("total_transactions", int64(total)),
		attribute.Int("total_pages", int(math.Ceil(float64(total)/float64(limit)))),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Transactions listed successfully",
		ion.Int("page", page),
		ion.Int("limit", limit),
		ion.Int("transactions_returned", len(transactions)),
		ion.Int("total_transactions", total),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.listTransactions"))

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"transactions": transactions,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}

// From last block number - go in reverse order and get the transactions in the block
// Returns paginated transactions from multiple blocks starting from the latest block going backwards
func (s *ImmuDBServer) listTransactions_fromLastBlock(c *gin.Context) {
	// Create span for listTransactions_fromLastBlock
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.listTransactions_fromLastBlock")
	defer span.End()

	startTime := time.Now().UTC()

	// Get and validate pagination parameters
	pageStr := c.DefaultQuery("page", "1")
	limitStr := c.DefaultQuery("limit", "20")

	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		page = 1
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100 // Maximum limit to prevent excessive memory usage
	}

	span.SetAttributes(
		attribute.Int("page", page),
		attribute.Int("limit", limit),
	)

	// Get the last block number
	lastBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to get last block number",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.listTransactions_fromLastBlock"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get last block number"})
		return
	}

	// Calculate how many transactions we need to collect
	// We need enough for the current page: (page - 1) * limit + limit = page * limit
	transactionsNeeded := page * limit

	// Collect transactions from blocks starting from latest, going backwards
	var allTransactions []*config.Transaction
	currentBlock := lastBlockNumber
	maxBlocksToScan := uint64(1000) // Safety limit to prevent infinite loops
	blocksScanned := uint64(0)

	// Collect transactions until we have enough for the requested page
	for len(allTransactions) < transactionsNeeded && currentBlock > 0 && blocksScanned < maxBlocksToScan {
		blockTransactions, err := DB_OPs.GetTransactionsOfBlock(&s.defaultdb, currentBlock)
		if err != nil {
			// Log error but continue to next block
			s.defaultdb.Client.Logger.Warn(spanCtx, "Failed to get transactions from block, skipping",
				ion.String("error", err.Error()),
				ion.Uint64("block_number", currentBlock),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "ExplorerAPI.listTransactions_fromLastBlock"))
			currentBlock--
			blocksScanned++
			continue
		}

		// Append transactions from this block (they're already in order from the block)
		allTransactions = append(allTransactions, blockTransactions...)
		currentBlock--
		blocksScanned++
	}

	// If no transactions found
	if len(allTransactions) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"latest_block_number": lastBlockNumber,
			"transactions":        []*config.Transaction{},
			"pagination": gin.H{
				"current_page": page,
				"per_page":     limit,
				"total_pages":  0,
				"total_items":  0,
				"has_next":     false,
				"has_prev":     false,
			},
		})
		return
	}

	// Calculate total transactions across all blocks for accurate pagination
	// We need to count all transactions from all blocks (or at least estimate)
	// For now, we'll use the count of transactions we've collected as an estimate
	// A more accurate approach would scan all blocks, but that's expensive
	total := len(allTransactions)

	// If we haven't reached the end, we need to count remaining transactions
	// For efficiency, we'll estimate based on blocks scanned
	// If we hit the limit, there might be more transactions
	hasMoreBlocks := currentBlock > 0
	if hasMoreBlocks {
		// Estimate: if we scanned blocks and got transactions, estimate there are more
		// For accurate count, we'd need to scan all blocks (expensive operation)
		// For now, we'll indicate there might be more by checking if we hit the scan limit
		if blocksScanned >= maxBlocksToScan {
			// We hit the safety limit, so there are likely more transactions
			// Set total to a value that indicates "more available"
			total = len(allTransactions) + 1 // Indicate there's at least one more
		}
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	offset := (page - 1) * limit

	// Ensure offset is within bounds
	if offset >= len(allTransactions) {
		// Requested page is beyond available transactions
		c.JSON(http.StatusOK, gin.H{
			"latest_block_number": lastBlockNumber,
			"transactions":        []*config.Transaction{},
			"pagination": gin.H{
				"current_page": page,
				"per_page":     limit,
				"total_pages":  totalPages,
				"total_items":  len(allTransactions),
				"has_next":     false,
				"has_prev":     page > 1,
			},
		})
		return
	}

	// Extract paginated slice
	var paginatedTransactions []*config.Transaction
	if offset < len(allTransactions) {
		end := offset + limit
		if end > len(allTransactions) {
			end = len(allTransactions)
		}
		paginatedTransactions = allTransactions[offset:end]
	}

	// Determine if there are more pages
	hasNext := offset+limit < len(allTransactions) || hasMoreBlocks

	span.SetAttributes(
		attribute.String("status", "success"),
		attribute.Int("transactions_returned", len(paginatedTransactions)),
		attribute.Int("total_transactions", len(allTransactions)),
		attribute.Int64("blocks_scanned", int64(blocksScanned)),
		attribute.Int64("latest_block_number", int64(lastBlockNumber)),
		attribute.Int("total_pages", totalPages),
	)
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Transactions from last block listed successfully",
		ion.Int("page", page),
		ion.Int("limit", limit),
		ion.Int("transactions_returned", len(paginatedTransactions)),
		ion.Int("total_transactions", len(allTransactions)),
		ion.Uint64("blocks_scanned", blocksScanned),
		ion.Uint64("latest_block_number", lastBlockNumber),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.listTransactions_fromLastBlock"))

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"latest_block_number": lastBlockNumber,
		"blocks_scanned":      blocksScanned,
		"transactions":        paginatedTransactions,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  totalPages,
			"total_items":  len(allTransactions),
			"has_next":     hasNext,
			"has_prev":     page > 1,
		},
	})
}
