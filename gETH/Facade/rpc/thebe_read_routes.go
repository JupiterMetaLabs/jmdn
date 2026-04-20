package rpc

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

const (
	thebeDefaultListLimit = 50
	thebeMaxListLimit     = 500
)

// registerThebeReadRoutes exposes Cassata / Postgres projection reads under /debug/thebe/.
// When Thebe is disabled (no Cassata on HTTPServer), handlers return 503 except /status.
func (s *HTTPServer) registerThebeReadRoutes(router gin.IRoutes) {
	router.GET("/debug/thebe/status", s.thebeStatus)
	// Register more specific block routes before /blocks/:num
	router.GET("/debug/thebe/blocks/:num/transactions", s.thebeBlockTransactions)
	router.GET("/debug/thebe/blocks/:num/zkproof", s.thebeBlockZKProof)
	router.GET("/debug/thebe/blocks/:num/snapshot", s.thebeBlockSnapshot)
	router.GET("/debug/thebe/blocks/:num", s.thebeGetBlock)
	router.GET("/debug/thebe/blocks", s.thebeListBlocks)
	router.GET("/debug/thebe/transactions/:txHash", s.thebeGetTx)
	router.GET("/debug/thebe/accounts/:address/nonce", s.thebeGetAccountNonce)
	router.GET("/debug/thebe/accounts/:address/transactions", s.thebeAccountTransactions)
	router.GET("/debug/thebe/accounts/:address", s.thebeGetAccount)
	router.GET("/debug/thebe/accounts", s.thebeListAccounts)
	router.GET("/debug/thebe/snapshots", s.thebeListSnapshots)
}

func (s *HTTPServer) thebeStatus(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, gin.H{"enabled": s.cassata != nil})
}

func (s *HTTPServer) thebeListAccounts(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	limit, offset, ok := thebeParseLimitOffset(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	rows, err := s.cassata.ListAccounts(ctx, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *HTTPServer) thebeGetAccount(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	addr := normalizeHexAddress(c.Param("address"))
	if !common.IsHexAddress(addr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid address"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	a, err := s.cassata.GetAccount(ctx, addr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, a)
}

func (s *HTTPServer) thebeGetAccountNonce(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	addr := normalizeHexAddress(c.Param("address"))
	if !common.IsHexAddress(addr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid address"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	a, err := s.cassata.GetAccount(ctx, addr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"address": a.Address,
		"nonce":   a.Nonce,
	})
}

func (s *HTTPServer) thebeAccountTransactions(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	addr := normalizeHexAddress(c.Param("address"))
	if !common.IsHexAddress(addr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid address"})
		return
	}
	limit, offset, ok := thebeParseLimitOffset(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	rows, err := s.cassata.ListTransactionsByAddress(ctx, addr, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *HTTPServer) thebeListBlocks(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	limit, offset, ok := thebeParseLimitOffset(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	rows, err := s.cassata.ListBlocks(ctx, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *HTTPServer) thebeGetBlock(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	n, ok := thebeParseUint64Param(c, "num")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	b, err := s.cassata.GetBlock(ctx, n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, b)
}

func (s *HTTPServer) thebeBlockTransactions(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	n, ok := thebeParseUint64Param(c, "num")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	rows, err := s.cassata.ListTransactionsByBlock(ctx, n)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *HTTPServer) thebeBlockZKProof(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	n, ok := thebeParseUint64Param(c, "num")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	z, err := s.cassata.GetZKProofByBlock(ctx, n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, z)
}

func (s *HTTPServer) thebeBlockSnapshot(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	n, ok := thebeParseUint64Param(c, "num")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	snap, err := s.cassata.GetSnapshot(ctx, n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (s *HTTPServer) thebeGetTx(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	h := normalizeTxHash(c.Param("txHash"))
	if !looksLikeTxHash(h) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tx hash"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	t, err := s.cassata.GetTransaction(ctx, h)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

func (s *HTTPServer) thebeListSnapshots(c *gin.Context) {
	if s.cassata == nil {
		c.String(http.StatusServiceUnavailable, "thebe read api not enabled")
		return
	}
	limit, offset, ok := thebeParseLimitOffset(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	rows, err := s.cassata.ListSnapshots(ctx, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func thebeParseLimitOffset(c *gin.Context) (limit, offset int, ok bool) {
	limit = thebeDefaultListLimit
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return 0, 0, false
		}
		if n > thebeMaxListLimit {
			n = thebeMaxListLimit
		}
		limit = n
	}
	if v := c.Query("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func thebeParseUint64Param(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return n, true
}

func normalizeHexAddress(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if !strings.HasPrefix(s, "0x") && len(s) == 40 {
		s = "0x" + s
	}
	return s
}

func normalizeTxHash(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if !strings.HasPrefix(s, "0x") && len(s) == 64 {
		s = "0x" + s
	}
	return s
}

func looksLikeTxHash(h string) bool {
	h = strings.TrimSpace(strings.ToLower(h))
	if !strings.HasPrefix(h, "0x") {
		return false
	}
	raw := h[2:]
	if len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}
