package explorer

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"gossipnode/DB_OPs"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

// AddressSummary represents a summary of an address
type AddressSummary struct {
	Address     string `json:"address"`
	Balance     string `json:"balance"`
	Nonce       uint64 `json:"nonce"`
	AccountType string `json:"account_type"`
	DIDAddress  string `json:"did_address,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// AddressDetails represents detailed information about an address
type AddressDetails struct {
	AddressSummary
	TransactionCount int64                  `json:"transaction_count"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// getAddressTransactions returns transactions for a specific address
// Uses database-level pagination to avoid loading all transactions into memory,
// making it much faster for accounts with many transactions.
func (s *ImmuDBServer) getAddressTransactions(c *gin.Context) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addressParam := c.Param("address")

	// Validate address format
	if !common.IsHexAddress(addressParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid address format"})
		return
	}

	address := common.HexToAddress(addressParam)

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

	// Calculate offset for pagination
	offset := (page - 1) * limit

	// Get paginated transactions using the optimized paginated function
	// This function scans blocks in reverse order and stops early once it has enough transactions
	transactions, total, err := DB_OPs.GetTransactionsByAccountPaginated(&s.defaultdb, &address, offset, limit)
	if err != nil {
		logger().GetNamedLogger().Error(loggerCtx, "Failed to get transactions for address",
			err,
			ion.String("address", addressParam),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "Explorer.getAddressTransactions"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions"})
		return
	}

	// Calculate pagination metadata
	totalPages := 0
	hasNext := false
	if total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(limit)))
		hasNext = offset+limit < total
	}

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"transactions": transactions,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  totalPages,
			"total_items":  total,
			"has_next":     hasNext,
			"has_prev":     page > 1,
		},
	})
}
