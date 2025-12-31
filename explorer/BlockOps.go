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
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/logging"

	explorer_common "gossipnode/explorer/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var BlockOpsLocalGRO interfaces.LocalGoroutineManagerInterface

type stats struct {
	DBState           *schema.ImmutableState
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
	number := c.Param("number")
	numberInt, err := ConvertStringToUint64(number)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}
	block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, numberInt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, block)
}

// Get block by hash
func (s *ImmuDBServer) getBlock(c *gin.Context) {
	hash := c.Param("id")
	block, err := DB_OPs.GetZKBlockByHash(&s.defaultdb, hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, block)
}

// List all blocks by pagination
func (s *ImmuDBServer) listBlocks(c *gin.Context) {
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

	// Get total number of blocks for pagination metadata
	totalBlocks, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get total blocks count"})
		return
	}

	// Calculate offset and limit for pagination
	offset := (page - 1) * limit
	endBlock := totalBlocks - uint64(offset)
	startBlock := uint64(1)
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
			s.defaultdb.Client.Logger.Logger.Error("Failed to get block %d, skipping: %v",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "Explorer.listBlocks"),
			)
			continue
		}
		blocks = append(blocks, block)
	}

	// Calculate pagination metadata
	totalPages := (int(totalBlocks) + limit - 1) / limit
	hasNext := page < totalPages
	hasPrev := page > 1

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

func (s *ImmuDBServer) getTransactionBlock(c *gin.Context) {
	hash := c.Param("hash")
	block, err := DB_OPs.GetTransactionBlock(&s.defaultdb, hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, block)
}

func (s *ImmuDBServer) getLatestBlock(c *gin.Context) {
	// Get the latest block number
	latestBlockNumber, err := GetLatesBlockNumber(s)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Get the latest block by number
	block, err := GetLatestBlockByNumber(s, latestBlockNumber)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, block)
}

func (s *ImmuDBServer) getLatestBlockStats(c *gin.Context) {
	latestBlockNumber, err := GetLatesBlockNumber(s)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	block, err := GetLatestBlockByNumber(s, latestBlockNumber)
	if err != nil {
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
	fmt.Println("latestBlockStats", latestBlockStats)
	c.JSON(http.StatusOK, latestBlockStats)
}

// Get transaction by hash
func (s *ImmuDBServer) getTransaction(c *gin.Context) {
	hash := c.Param("hash")
	transaction, err := DB_OPs.GetTransactionByHash(&s.defaultdb, hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, transaction)
}

// Get list of transactions in a given block
func (s *ImmuDBServer) listTransactions_inBlock(c *gin.Context) {
	blockNumber := c.Param("number")
	blockNumberInt, err := ConvertStringToUint64(blockNumber)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}
	BlockData, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, blockNumberInt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	Transactions := make([]*config.Transaction, 0)

	for _, tx := range BlockData.Transactions {
		Transactions = append(Transactions, &tx)
	}
	c.JSON(http.StatusOK, Transactions)
}

// Get default db stats
func (s *ImmuDBServer) getStats(c *gin.Context) {
	if BlockOpsLocalGRO == nil {
		var err error
		BlockOpsLocalGRO, err = explorer_common.InitializeGRO(GRO.ExplorerBlockOpsLocal)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Errorf("failed to initialize local gro: %v", err)})
			return
		}
	}
	// Create a context with 60 second timeout
	ctxWithTimeout, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
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
		tempClient := s.defaultdb.Client
		status, err := DB_OPs.GetDatabaseState(tempClient)
		if err != nil {
			handleErr(fmt.Errorf("failed to get database state: %w", err))
			return nil
		}
		mu.Lock()
		stats.DBState = status
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, stats)
}

func (s *ImmuDBServer) getDIDDetailsFromAddr(c *gin.Context) {
	// Change from c.Param to c.Query since addr is a query parameter
	addr := c.Query("addr")
	if addr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "addr query parameter is required"})
		return
	}

	fmt.Println("addr", addr)

	DIDDocument, err := DB_OPs.GetAccount(&s.accountsdb, common.HexToAddress(addr))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, DIDDocument)
}

// Get the Missing blocks
func (s *ImmuDBServer) getMissingBlocks(c *gin.Context) {
	number := c.Param("number")
	blockNumberInt, err := ConvertStringToUint64(number)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
		return
	}
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if blockNumberInt > latestBlockNumber {
		c.JSON(http.StatusBadRequest, gin.H{"error": "block number is greater than latest block number"})
		return
	}
	var missingBlocks []*config.ZKBlock
	for blockNumberInt < latestBlockNumber {
		blockNumberInt++
		block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, blockNumberInt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		missingBlocks = append(missingBlocks, block)
	}
	c.JSON(http.StatusOK, missingBlocks)
}

func (s *ImmuDBServer) listTransactions(c *gin.Context) {
	// Get pagination parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10 // Default to 10 items per page, max 100
	}

	// Calculate offset
	offset := (page - 1) * limit

	// Get paginated transactions directly from database (database-level pagination)
	transactions, total, err := DB_OPs.GetTransactionsPaginated(&s.defaultdb, offset, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions: " + err.Error()})
		return
	}

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

	// Get the last block number
	lastBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		s.defaultdb.Client.Logger.Logger.Error("Failed to get last block number",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.listTransactions_fromLastBlock"),
		)
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
			s.defaultdb.Client.Logger.Logger.Warn("Failed to get transactions from block, skipping",
				zap.Error(err),
				zap.Uint64("block_number", currentBlock),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, config.LOKI_URL),
				zap.String(logging.Function, "Explorer.listTransactions_fromLastBlock"),
			)
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
