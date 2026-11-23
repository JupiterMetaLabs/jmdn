package rpc

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/internal/pendingcommit"
	"gossipnode/internal/roundlock"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/pkg/gatekeeper"
)

// RegisterConsensusAdminRoutes adds the admin-only consensus recovery
// endpoints:
//
//	POST /consensus/round-ledger/clear {"height": N, "include_signing_ledger": false}
//
// clears this node's vote ledger (roundlock.Votes) for one height above the
// committed tip, so a height whose retries the ledger refuses can commit
// again. The result-signing ledger (roundlock.Default) is cleared only with
// include_signing_ledger: true. That ledger is what stops this node signing
// two blocks in one round; clear it only when the height is known to have no
// certificate anywhere, including one a sequencer may be withholding. Every
// cleared round is logged with its block identity. With no config loaded
// nothing is authorized.
//
//	GET  /consensus/apply-halt
//	POST /consensus/apply-halt/clear
//
// report and lift this node's block-apply halt (BlockProcessing.ErrApplyHalted):
// set when the node holds a block it could not roll back, recorded so a
// restart does not lift it. Clear it only once the node's state is again the
// canonical chain's, after a resync (docs/block-apply-halt.md).
//
//	GET  /consensus/pending-commit
//	POST /consensus/pending-commit/drop {"height": N, "block_hash": "0x..."}
//	POST /consensus/pending-commit/drop {"unreadable": true}
//
// report and, as a last resort, drop the sequencer's pending certified-block
// record. While it exists the block servers refuse proposals at that height
// and above, and the block is retried in the background. Drop it only when
// that block can never be stored here (for example a different or partly
// applied block is stored, or this node's block apply is halted and no peer
// holds the block) AND the fleet has agreed how the height continues: once
// the record is gone a different block can be certified at the height while
// this block's certificate still exists. Height and block hash must both
// match the record. The drop is logged with the full record identity. A
// stored record that cannot be decoded (status reports "unreadable": true)
// keeps proposals refused; {"unreadable": true} removes it, and only then.
func RegisterConsensusAdminRoutes(router gin.IRouter) {
	var secCfg *settings.SecurityConfig
	if settings.IsLoaded() {
		secCfg = &settings.Get().Security
	}
	router.POST("/consensus/round-ledger/clear", gatekeeper.AdminHTTPMiddleware(secCfg), handleClearRoundLedger)
	router.GET("/consensus/apply-halt", gatekeeper.AdminHTTPMiddleware(secCfg), handleApplyHaltStatus)
	router.POST("/consensus/apply-halt/clear", gatekeeper.AdminHTTPMiddleware(secCfg), handleClearApplyHalt)
	router.GET("/consensus/pending-commit", gatekeeper.AdminHTTPMiddleware(secCfg), handlePendingCommitStatus)
	router.POST("/consensus/pending-commit/drop", gatekeeper.AdminHTTPMiddleware(secCfg), handleDropPendingCommit)
}

// handlePendingCommitStatus reports the pending certified-block record.
func handlePendingCommitStatus(c *gin.Context) {
	rec, err := pendingcommit.Default.Current()
	if errors.Is(err, pendingcommit.ErrUnreadable) {
		// Proposals are refused while the record cannot be read.
		c.JSON(http.StatusOK, gin.H{"pending": true, "unreadable": true, "error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rec == nil {
		c.JSON(http.StatusOK, gin.H{"pending": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"pending":    true,
		"height":     rec.Height,
		"block_hash": rec.BlockHash,
		"created_at": rec.CreatedAt,
	})
}

// pendingCommitDropper drops the pending record. The sequencer wires
// Sequencer.DropPendingCommit, which coordinates with an in-flight commit
// attempt and reports the dropped block to the orchestrator. Unwired (no
// sequencer in this process), the record is dropped directly.
var (
	pendingCommitDropMu  sync.Mutex
	pendingCommitDropper func(height uint64, blockHash string) (*pendingcommit.Record, error)
)

// pendingCommitUnreadableDropper removes an undecodable record; the
// sequencer wires Sequencer.DropUnreadablePendingCommit.
var pendingCommitUnreadableDropper func() error

// SetPendingCommitDropper wires the drop functions (by height and hash, and
// for an undecodable record). Call once at startup.
func SetPendingCommitDropper(fn func(height uint64, blockHash string) (*pendingcommit.Record, error), unreadable func() error) {
	pendingCommitDropMu.Lock()
	pendingCommitDropper = fn
	pendingCommitUnreadableDropper = unreadable
	pendingCommitDropMu.Unlock()
}

type dropPendingCommitRequest struct {
	Height     *uint64 `json:"height"`
	BlockHash  string  `json:"block_hash"`
	Unreadable bool    `json:"unreadable"`
}

// handleDropPendingCommit removes the pending record when height and block
// hash both match it. See RegisterConsensusAdminRoutes for when this is safe.
func handleDropPendingCommit(c *gin.Context) {
	var req dropPendingCommitRequest
	bindErr := c.ShouldBindJSON(&req)
	if bindErr == nil && req.Unreadable {
		handleDropUnreadablePendingCommit(c)
		return
	}
	if bindErr != nil || req.Height == nil || strings.TrimSpace(req.BlockHash) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be {"height": <block number>, "block_hash": "<hash of the pending block>"} or {"unreadable": true}`})
		return
	}
	pendingCommitDropMu.Lock()
	drop := pendingCommitDropper
	pendingCommitDropMu.Unlock()
	if drop == nil {
		drop = pendingcommit.Default.DropMatching
	}
	rec, err := drop(*req.Height, strings.TrimSpace(req.BlockHash))
	switch {
	case errors.Is(err, pendingcommit.ErrNoPending):
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending certified block"})
		return
	case errors.Is(err, pendingcommit.ErrRecordMismatch):
		resp := gin.H{"error": "height and block_hash must match the pending record"}
		if rec != nil {
			resp["pending_height"] = rec.Height
			resp["pending_block_hash"] = rec.BlockHash
		}
		c.JSON(http.StatusConflict, resp)
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator dropped the pending certified-block record; a different block may now be certified at this height",
			ion.Uint64("height", rec.Height),
			ion.String("block_hash", rec.BlockHash),
			ion.Int64("created_at", rec.CreatedAt))
	}
	c.JSON(http.StatusOK, gin.H{"dropped": true, "height": rec.Height, "block_hash": rec.BlockHash})
}

// handleApplyHaltStatus reports the halt. recorded=false while halted means
// the halt is not yet in the node's store, so a restart now would lift it.
func handleApplyHaltStatus(c *gin.Context) {
	reason, recorded, err := BlockProcessing.ApplyHaltStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"halted": reason != "", "reason": reason, "recorded": recorded})
}

func handleClearApplyHalt(c *gin.Context) {
	reason, err := BlockProcessing.ClearApplyHalt()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if reason == "" {
		c.JSON(http.StatusOK, gin.H{"cleared": false, "previous_reason": ""})
		return
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator cleared the block-apply halt",
			ion.String("previous_reason", reason))
	}
	c.JSON(http.StatusOK, gin.H{"cleared": true, "previous_reason": reason})
}

type clearRoundLedgerRequest struct {
	Height               *uint64 `json:"height"`
	IncludeSigningLedger bool    `json:"include_signing_ledger"`
}

// describeCleared renders period -> block identity in period order.
func describeCleared(m map[uint64]string) string {
	periods := make([]uint64, 0, len(m))
	for p := range m {
		periods = append(periods, p)
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i] < periods[j] })
	parts := make([]string, 0, len(periods))
	for _, p := range periods {
		parts = append(parts, fmt.Sprintf("%d=%s", p, m[p]))
	}
	return strings.Join(parts, ",")
}

func handleClearRoundLedger(c *gin.Context) {
	var req clearRoundLedgerRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Height == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be {"height": <block number>}`})
		return
	}
	h := *req.Height
	// A certified block at this height is still being committed: clearing the
	// signing records would reopen rounds while a valid certificate exists.
	// Fail closed when the pending record cannot be read.
	if pending, perr := pendingcommit.Default.PendingAt(h); pending {
		msg := fmt.Sprintf("height %d has a certified block that is still being committed; repair the cause and let it commit instead", h)
		if perr != nil {
			msg = fmt.Sprintf("cannot read the pending-commit record (%v); refusing to clear height %d", perr, h)
		}
		c.JSON(http.StatusConflict, gin.H{"error": msg, "reason": "height_pending_commit"})
		return
	}
	votes, err := roundlock.Votes.ClearHeight(h)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	var signer map[uint64]string
	if req.IncludeSigningLedger {
		if signer, err = roundlock.Default.ClearHeight(h); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "votes_cleared": describeCleared(votes)})
			return
		}
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator cleared round-ledger records for a height",
			ion.Uint64("height", h),
			ion.String("votes_cleared", describeCleared(votes)),
			ion.Bool("signing_ledger_included", req.IncludeSigningLedger),
			ion.String("signer_cleared", describeCleared(signer)))
	}
	c.JSON(http.StatusOK, gin.H{
		"height":                  h,
		"votes_cleared":           describeCleared(votes),
		"signing_ledger_included": req.IncludeSigningLedger,
		"signer_cleared":          describeCleared(signer),
	})
}

// handleDropUnreadablePendingCommit removes a stored pending record that
// cannot be decoded. A readable record is refused (409): drop it by height
// and block hash instead.
func handleDropUnreadablePendingCommit(c *gin.Context) {
	pendingCommitDropMu.Lock()
	drop := pendingCommitUnreadableDropper
	pendingCommitDropMu.Unlock()
	if drop == nil {
		drop = pendingcommit.Default.DropUnreadable
	}
	err := drop()
	switch {
	case errors.Is(err, pendingcommit.ErrNoPending):
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending certified block"})
		return
	case errors.Is(err, pendingcommit.ErrReadable):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator dropped an unreadable pending certified-block record; a different block may now be certified at its height")
	}
	c.JSON(http.StatusOK, gin.H{"dropped": true, "unreadable": true})
}
