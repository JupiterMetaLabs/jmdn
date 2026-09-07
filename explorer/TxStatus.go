package explorer

// Tron-style transaction lifecycle status for the explorer.
//
// GET /api/block/transactions/:hash/status returns a coarse progress stage
// (QUEUED -> PENDING -> EXECUTING -> SUCCESS/FAILED) plus live consensus counts
// while a block is being agreed. Resolution precedence is CHAIN FIRST: a
// transaction present in a stored block is authoritatively SUCCESS regardless of
// any stale in-memory lifecycle entry; only when it is not yet on chain do we
// fall back to the explorer's in-memory lifecycle registry (fed by thin
// notifications from the propose/vote/commit path — see explorer/lifecycle).
//
// This endpoint READS only. It never mutates consensus state and does not touch
// the geth-facade tx-status feature.

import (
	"net/http"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/explorer/lifecycle"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
)

// txStatusResponse is the client-facing shape.
type txStatusResponse struct {
	Hash        string              `json:"hash"`
	Status      lifecycle.Stage     `json:"status"`
	Found       bool                `json:"found"`
	Source      string              `json:"source"` // "chain" | "registry" | "none"
	BlockNumber uint64              `json:"block_number,omitempty"`
	Consensus   *lifecycle.Progress `json:"consensus,omitempty"`
	Reason      string              `json:"reason,omitempty"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
}

// StatusUnknown is reported when the hash is neither on chain nor in the
// lifecycle registry (never seen, or its registry entry has expired).
const statusUnknown lifecycle.Stage = "UNKNOWN"

func (s *ExplorerServer) getTransactionStatus(c *gin.Context) {
	_, span := logger().Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getTransactionStatus")
	defer span.End()

	hash := c.Param("hash")
	span.SetAttributes(attribute.String("transaction_hash", hash))

	// 1. CHAIN FIRST — a tx in a stored block is terminally SUCCESS. This is the
	//    durable truth and always wins over any in-memory lifecycle entry.
	if tx, err := DB_OPs.GetTransactionByHash(&s.defaultdb, hash); err == nil && tx != nil {
		resp := txStatusResponse{
			Hash:   hash,
			Status: lifecycle.StageSuccess,
			Found:  true,
			Source: "chain",
		}
		span.SetAttributes(attribute.String("status", "success"), attribute.String("tx_stage", string(resp.Status)))
		c.JSON(http.StatusOK, resp)
		return
	}
	// A not-found chain read is the normal pre-commit case — fall through to the
	// live registry rather than treating it as an error.

	// 2. LIVE REGISTRY — pre-commit stages with consensus counts.
	if e, ok := lifecycle.Get(hash); ok {
		resp := txStatusResponse{
			Hash:        e.Hash,
			Status:      e.Stage,
			Found:       true,
			Source:      "registry",
			BlockNumber: e.BlockNumber,
			Reason:      e.Reason,
			UpdatedAt:   e.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if e.Stage == lifecycle.StagePending || e.Stage == lifecycle.StageExecuting {
			p := e.Progress
			resp.Consensus = &p
		}
		span.SetAttributes(attribute.String("status", "success"), attribute.String("tx_stage", string(resp.Status)))
		c.JSON(http.StatusOK, resp)
		return
	}

	// 3. Unknown — never seen here, or the live entry expired before it committed.
	span.SetAttributes(attribute.String("status", "success"), attribute.String("tx_stage", string(statusUnknown)))
	c.JSON(http.StatusOK, txStatusResponse{
		Hash:   hash,
		Status: statusUnknown,
		Found:  false,
		Source: "none",
	})
}
