package explorer

import (
	"context"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"

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

// addrTxHydrationConcurrency bounds how many ImmuDB point-fetches run in
// parallel per page request. Page size is capped at 100, so this keeps
// per-request connection-pool pressure bounded while still cutting p99
// latency versus a fully sequential loop.
const addrTxHydrationConcurrency = 10

// maxAddrTxPage is a hard ceiling on the requested page number, independent
// of the total-count short-circuit below — belt-and-suspenders against a
// pathological (page-1)*limit computation before we've even looked up total.
const maxAddrTxPage = 1_000_000

// getAddressTransactions returns transactions for a specific address.
// Uses SQLite txindex for O(log n) lookup → ImmuDB point-fetch per page item.
// If the txindex is unavailable, this returns 503 rather than a fake empty
// "no transactions" result — callers must be able to tell "no data" apart
// from "data source down".
func (s *ImmuDBServer) getAddressTransactions(c *gin.Context) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// reqCtx is tied to the HTTP request's lifecycle (cancelled if the client
	// disconnects) — pass it into txindex calls instead of a fresh Background
	// context so a dropped connection actually aborts the in-flight query.
	reqCtx := c.Request.Context()
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
	if page > maxAddrTxPage {
		page = maxAddrTxPage
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	// Look up the total first: a page far beyond the last one would otherwise
	// force SQLite into an O(offset) skip-scan (OFFSET is not O(1) even with
	// the covering index) for a request that's cheap to issue — check the
	// count and short-circuit to an empty page instead of paying for the scan.
	total, totalErr := txindex.CountByAddress(reqCtx, normalizedAddr)
	if totalErr != nil {
		logger().GetNamedLogger().Error(loggerCtx, "txindex unavailable",
			totalErr,
			ion.String("address", addressParam),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "Explorer.getAddressTransactions"))
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "transaction index temporarily unavailable, please retry",
		})
		return
	}
	if offset < 0 || offset >= total {
		c.JSON(http.StatusOK, gin.H{
			"transactions": []interface{}{},
			"pagination": gin.H{
				"current_page": page,
				"per_page":     limit,
				"total_pages":  totalPagesOf(total, limit),
				"total_items":  total,
				"has_next":     false,
				"has_prev":     page > 1,
			},
		})
		return
	}

	// ── Fast path via SQLite txindex ─────────────────────────────────────────
	refs, idxErr := txindex.QueryByAddressOffset(reqCtx, normalizedAddr, offset, limit)
	if idxErr != nil {
		// txindex down/uninitialised: this is a service outage, not "address
		// has no transactions". Returning 200+empty here would silently lie
		// to explorers/wallets. Surface it as 503 so callers retry instead of
		// showing a wrong "no activity" result.
		logger().GetNamedLogger().Error(loggerCtx, "txindex unavailable",
			idxErr,
			ion.String("address", addressParam),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "Explorer.getAddressTransactions"))
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "transaction index temporarily unavailable, please retry",
		})
		return
	}

	// total was already computed above (used for the out-of-range short-circuit).

	// Hydrate each ref from ImmuDB by hash — only page-size hits (≤100) —
	// in parallel (bounded) since each fetch is an independent point lookup.
	type txItem struct {
		BlockNumber uint64 `json:"block_number"`
		TxHash      string `json:"tx_hash"`
	}
	type hydrated struct {
		BlockNumber    uint64 `json:"block_number"`
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
	}

	items := make([]interface{}, len(refs))
	sem := make(chan struct{}, addrTxHydrationConcurrency)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ref txindex.TxRef) {
			defer wg.Done()
			defer func() { <-sem }()

			tx, fetchErr := DB_OPs.GetTransactionByHash(&s.defaultdb, ref.TxHash)
			if fetchErr != nil || tx == nil {
				// Index references a tx we can't fetch — include skeleton so
				// the caller can see the block_number/hash even without full data.
				items[i] = txItem{BlockNumber: ref.BlockNumber, TxHash: ref.TxHash}
				return
			}
			items[i] = hydrated{
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
			}
		}(i, ref)
	}
	wg.Wait()

	c.JSON(http.StatusOK, gin.H{
		"transactions": items,
		"pagination": gin.H{
			"current_page": page,
			"per_page":     limit,
			"total_pages":  totalPagesOf(total, limit),
			"total_items":  total,
			"has_next":     offset+limit < total,
			"has_prev":     page > 1,
		},
	})
}

// totalPagesOf computes the page count for a given total item count and page size.
func totalPagesOf(total, limit int) int {
	if total <= 0 || limit <= 0 {
		return 0
	}
	return int(math.Ceil(float64(total) / float64(limit)))
}
