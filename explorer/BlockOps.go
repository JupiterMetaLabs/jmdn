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

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/gin-gonic/gin"
)

type stats struct {
	DBState    *schema.ImmutableState
	MerkleRoot string
	LatestBlockNumber uint64
	TotalBlocks uint64
	TotalDIDs	int64
	TotalTransactions int64
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
			config.Warning(s.defaultdb.Logger, "Failed to get block %d, skipping: %v", i, err)
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
	blockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, blockNumber)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, block)
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
	Transactions := make([]*config.ZKBlockTransaction, 0)

	for _, tx := range BlockData.Transactions {
		Transactions = append(Transactions, &tx)
	}
	c.JSON(http.StatusOK, Transactions)
}

// Get default db stats
func (s *ImmuDBServer) getStats(c *gin.Context) {
	// Create a context with 60 second timeout
	_, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	var stats stats
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Channel to collect errors from goroutines
	errChan := make(chan error, 4)
	
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		status, err := DB_OPs.GetDatabaseState(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to get database state: %w", err))
			return
		}
		mu.Lock()
		stats.DBState = status
		mu.Unlock()
	}()

	// Get merkle root in a goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		merkleroot, err := DB_OPs.GetMerkleRoot(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to get merkle root: %w", err))
			return
		}
		mu.Lock()
		stats.MerkleRoot = hex.EncodeToString(merkleroot)
		mu.Unlock()
	}()

	// Get latest block number and total blocks in a goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to get latest block number: %w", err))
			return
		}
		mu.Lock()
		stats.LatestBlockNumber = latestBlockNumber
		stats.TotalBlocks = latestBlockNumber+1
		mu.Unlock()

		// Get total transactions count efficiently
		totalTx, err := DB_OPs.CountTransactions(&s.defaultdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to count transactions: %w", err))
			return
		}
		mu.Lock()
		stats.TotalTransactions = int64(totalTx)
		mu.Unlock()
	}()

	// Get total DIDs in a goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		totalDIDs, err := DB_OPs.CountDIDs(&s.accountsdb)
		if err != nil {
			handleErr(fmt.Errorf("failed to list DIDs: %w", err))
			return
		}
		mu.Lock()
		stats.TotalDIDs = int64(totalDIDs)
		mu.Unlock()
	}()

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(errChan)
	}()

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
    
    DIDDocument, err := DB_OPs.GetDIDDocumentFromAddr(&s.accountsdb, addr)
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

    // Get paginated transaction hashes
    txHashes, total, err := DB_OPs.GetTransactionHashes(&s.defaultdb, offset, limit)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions"})
        return
    }

    // Fetch full transaction details
    var transactions []*config.ZKBlockTransaction
	transactions, err = DB_OPs.GetTransactionsBatch(&s.defaultdb, txHashes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transaction details"})
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