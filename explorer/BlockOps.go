package explorer

import (
	"encoding/hex"
	"net/http"
	"strconv"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/gin-gonic/gin"
)

type stats struct {
	DBState    *schema.ImmutableState
	MerkleRoot string
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
	var stats stats

	status, err := DB_OPs.GetDatabaseState(&s.defaultdb)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	merkleroot, err := DB_OPs.GetMerkleRoot(&s.defaultdb)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stats.DBState = status
	stats.MerkleRoot = hex.EncodeToString(merkleroot)

	c.JSON(http.StatusOK, stats)
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