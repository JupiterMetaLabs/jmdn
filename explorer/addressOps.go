package explorer

import (
	"context"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/txindex"

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

// addrHex converts a *common.Address to its hex string, returning "" for nil.
func addrHex(a *common.Address) string {
	if a == nil {
		return ""
	}
	return a.Hex()
}

// bigHex converts a *big.Int to its hex string ("0x…"), returning "" for nil.
func bigHex(n *big.Int) string {
	if n == nil {
		return ""
	}
	return "0x" + n.Text(16)
}

// getAddressTransactions returns transactions for a specific address.
// Uses SQLite txindex for O(log n) lookup → ImmuDB point-fetch per page item.
// Returns empty pagination if txindex is not initialised.
func (s *ImmuDBServer) getAddressTransactions(c *gin.Context) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addressParam := c.Param("address")

	if !common.IsHexAddress(addressParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid address format"})
		return
	}

	address := common.HexToAddress(addressParam)
	normalizedAddr := strings.ToLower(address.Hex()) // lowercase — matches ImmuDB/SQLite storage

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
		limit = 100
	}
	offset := (page - 1) * limit

	// ── Fast path via SQLite txindex ─────────────────────────────────────────
	refs, idxErr := txindex.QueryByAddressOffset(normalizedAddr, offset, limit)
	if idxErr == nil {
		total, _ := txindex.CountByAddress(normalizedAddr)

		// Hydrate each ref from ImmuDB by hash — only page-size hits (≤100).
		type txItem struct {
			BlockNumber uint64 `json:"block_number"`
			TxHash      string `json:"tx_hash"`
		}
		items := make([]interface{}, 0, len(refs))
		for _, ref := range refs {
			tx, fetchErr := DB_OPs.GetTransactionByHash(&s.defaultdb, ref.TxHash)
			if fetchErr != nil || tx == nil {
				// Index references a tx we can't fetch — include skeleton so
				// the caller can see the block_number/hash even without full data.
				items = append(items, txItem{BlockNumber: ref.BlockNumber, TxHash: ref.TxHash})
				continue
			}
			items = append(items, struct {
				BlockNumber uint64 `json:"block_number"`
				// flatten tx fields under the same object
				Hash           string `json:"hash"`
				From           string `json:"from,omitempty"`
				To             string `json:"to,omitempty"`
				Value          string `json:"value"`
				Nonce          uint64 `json:"nonce"`
				GasLimit       uint64 `json:"gas_limit"`
				GasPrice       string `json:"gas_price,omitempty"`
				MaxFee         string `json:"max_fee,omitempty"`
				MaxPriorityFee string `json:"max_priority_fee,omitempty"`
				Type           uint8  `json:"type"`
				Timestamp      uint64 `json:"timestamp"`
			}{
				BlockNumber:    ref.BlockNumber,
				Hash:           tx.Hash.Hex(),
				From:           addrHex(tx.From),
				To:             addrHex(tx.To),
				Value:          bigHex(tx.Value),
				Nonce:          tx.Nonce,
				GasLimit:       tx.GasLimit,
				GasPrice:       bigHex(tx.GasPrice),
				MaxFee:         bigHex(tx.MaxFee),
				MaxPriorityFee: bigHex(tx.MaxPriorityFee),
				Type:           tx.Type,
				Timestamp:      tx.Timestamp,
			})
		}

		totalPages := 0
		hasNext := false
		if total > 0 {
			totalPages = int(math.Ceil(float64(total) / float64(limit)))
			hasNext = offset+limit < total
		}
		c.JSON(http.StatusOK, gin.H{
			"transactions": items,
			"pagination": gin.H{
				"current_page": page,
				"per_page":     limit,
				"total_pages":  totalPages,
				"total_items":  total,
				"has_next":     hasNext,
				"has_prev":     page > 1,
			},
		})
		return
	}

	// txindex not initialised — return empty rather than hitting ImmuDB.
	logger().GetNamedLogger().Warn(loggerCtx, "txindex unavailable, returning empty result",
		ion.String("address", addressParam),
		ion.String("error", idxErr.Error()),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "Explorer.getAddressTransactions"))

	c.JSON(http.StatusOK, gin.H{
		"transactions": nil,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  0,
			"total_items":  0,
			"has_next":     false,
			"has_prev":     false,
		},
	})
}
