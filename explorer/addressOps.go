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

// listAddresses returns a paginated list of all addresses with their balances
func (s *ImmuDBServer) listAddresses(c *gin.Context) {
	// Get pagination parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20 // Default to 20 items per page, max 100
	}

	// Get accounts from database
	accounts, err := DB_OPs.ListAllAccounts(&s.accountsdb, 0) // 0 means no limit
	if err != nil {
		s.accountsdb.Client.Logger.Logger.Error("Failed to list accounts",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.listAddresses"),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch addresses"})
		return
	}

	// Convert to AddressSummary format
	var addressSummaries []AddressSummary
	for _, account := range accounts {
		summary := AddressSummary{
			Address:     account.Address.Hex(),
			Balance:     account.Balance,
			Nonce:       account.Nonce,
			AccountType: account.AccountType,
			DIDAddress:  account.DIDAddress,
			CreatedAt:   account.CreatedAt,
			UpdatedAt:   account.UpdatedAt,
		}
		addressSummaries = append(addressSummaries, summary)
	}

	// Calculate pagination
	total := len(addressSummaries)
	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	offset := (page - 1) * limit

	// Apply pagination
	var paginatedAddresses []AddressSummary
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedAddresses = addressSummaries[offset:end]
	}

	// Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"addresses": paginatedAddresses,
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

// getAddressDetails returns detailed information about a specific address
func (s *ImmuDBServer) getAddressDetails(c *gin.Context) {
	addressParam := c.Param("address")

	// Validate address format
	if !common.IsHexAddress(addressParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid address format"})
		return
	}

	address := common.HexToAddress(addressParam)

	// Get account details
	account, err := DB_OPs.GetAccount(&s.accountsdb, address)
	if err != nil {
		s.accountsdb.Client.Logger.Logger.Error("Failed to get account details",
			zap.Error(err),
			zap.String("address", addressParam),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.getAddressDetails"),
		)
		c.JSON(http.StatusNotFound, gin.H{"error": "Address not found"})
		return
	}

	// Get transaction count for this address
	transactionCount, err := DB_OPs.CountTransactionsByAccount(&s.defaultdb, &address)
	if err != nil {
		// Log error but don't fail the request
		s.defaultdb.Client.Logger.Logger.Warn("Failed to get transaction count",
			zap.Error(err),
			zap.String("address", addressParam),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, config.LOKI_URL),
			zap.String(logging.Function, "Explorer.getAddressDetails"),
		)
		transactionCount = 0
	}

	// Create detailed response
	details := AddressDetails{
		AddressSummary: AddressSummary{
			Address:     account.Address.Hex(),
			Balance:     account.Balance,
			Nonce:       account.Nonce,
			AccountType: account.AccountType,
			DIDAddress:  account.DIDAddress,
			CreatedAt:   account.CreatedAt,
			UpdatedAt:   account.UpdatedAt,
		},
		TransactionCount: transactionCount,
		Metadata:         account.Metadata,
	}

	c.JSON(http.StatusOK, details)
}

// getAddressTransactions returns transactions for a specific address
func (s *ImmuDBServer) getAddressTransactions(c *gin.Context) {
	addressParam := c.Param("address")

	// Validate address format
	if !common.IsHexAddress(addressParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid address format"})
		return
	}

	address := common.HexToAddress(addressParam)

	// Get pagination parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	// Get transactions for this address
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

	// Apply pagination
	total := len(transactions)
	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	offset := (page - 1) * limit

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
