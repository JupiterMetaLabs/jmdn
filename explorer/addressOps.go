package explorer

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/logging"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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
// Note: This function loads all transactions for the account into memory before paginating.
// For better performance with large datasets, consider implementing a paginated version
// of GetTransactionsByAccount at the database level.
func (s *ImmuDBServer) getAddressTransactions(c *gin.Context) {
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

	// Get transactions for this address using the main database connection
	// GetTransactionsByAccount scans all blocks to find transactions involving this account
	transactions, err := DB_OPs.GetTransactionsByAccount(&s.defaultdb, &address)
	if err != nil {
		s.defaultdb.Client.Logger.Logger.Error("Failed to get transactions for address",
			zap.Error(err),
			zap.String("address", addressParam),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.getAddressTransactions"),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch transactions"})
		return
	}

	// Apply pagination to the results
	total := len(transactions)
	if total == 0 {
		c.JSON(http.StatusOK, gin.H{
			"transactions": []*config.Transaction{},
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

	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	offset := (page - 1) * limit

	// Ensure offset is within bounds
	if offset >= total {
		offset = total - 1
		if offset < 0 {
			offset = 0
		}
	}

	// Extract paginated slice
	var paginatedTransactions []*config.Transaction
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedTransactions = transactions[offset:end]
	}

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"transactions": paginatedTransactions,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  totalPages,
			"total_items":  total,
			"has_next":     page < totalPages,
			"has_prev":     page > 1,
		},
	})
}
