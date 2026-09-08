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
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/explorer/lifecycle"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
)

// txStatusEventType is the StreamEvent.EventType carried on the shared SSE
// registry for lifecycle transitions.
const txStatusEventType = "tx_status"

// RegisterLifecycleObserver wires the lifecycle registry to the shared SSE
// registry so every stage transition is fanned out as a "tx_status" event. The
// per-hash stream handler filters these to one transaction. Called once at
// server construction.
func RegisterLifecycleObserver() {
	lifecycle.SetObserver(func(e lifecycle.Entry) {
		sendEventToClients(StreamEvent{EventType: txStatusEventType, Data: e})
	})
}

func normHash(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h != "" && !strings.HasPrefix(h, "0x") {
		h = "0x" + h
	}
	return h
}

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

// streamTxStatus is an SSE endpoint that pushes lifecycle transitions for ONE
// transaction hash. It subscribes to the shared stream registry (reused from the
// block stream) and forwards only "tx_status" events whose hash matches, then
// closes on a terminal stage. An initial snapshot is sent immediately so a
// late-connecting client sees the current stage without waiting for the next
// transition.
func (s *ExplorerServer) streamTxStatus(c *gin.Context) {
	rawHash := c.Param("hash")
	want := normHash(rawHash)

	messageChan := make(chan string, 64) // buffered so a brief slow read does not evict (audit API-02)
	streamRegistry.Lock()
	streamRegistry.clients[messageChan] = struct{}{}
	streamRegistry.Unlock()
	defer func() {
		streamRegistry.Lock()
		if _, ok := streamRegistry.clients[messageChan]; ok {
			close(messageChan)
			delete(streamRegistry.clients, messageChan)
		}
		streamRegistry.Unlock()
	}()

	notify := c.Writer.CloseNotify()

	// Initial snapshot: chain-first (terminal SUCCESS), then live registry, else UNKNOWN.
	{
		e := lifecycle.Entry{Hash: want, Stage: statusUnknown}
		if tx, err := DB_OPs.GetTransactionByHash(&s.defaultdb, rawHash); err == nil && tx != nil {
			e = lifecycle.Entry{Hash: want, Stage: lifecycle.StageSuccess}
		} else if cur, ok := lifecycle.Get(rawHash); ok {
			e = cur
		}
		if data, err := json.Marshal(StreamEvent{EventType: txStatusEventType, Data: e}); err == nil {
			c.SSEvent("message", string(data))
			c.Writer.Flush()
		}
	}

	for {
		select {
		case <-notify:
			return
		case msg, ok := <-messageChan:
			if !ok {
				return // evicted as too-slow
			}
			var ev struct {
				Event string          `json:"event"`
				Data  lifecycle.Entry `json:"data"`
			}
			if err := json.Unmarshal([]byte(msg), &ev); err != nil {
				continue
			}
			if ev.Event != txStatusEventType || normHash(ev.Data.Hash) != want {
				continue // not a tx_status event for this hash
			}
			c.SSEvent("message", msg)
			c.Writer.Flush()
			if ev.Data.Stage == lifecycle.StageSuccess || ev.Data.Stage == lifecycle.StageFailed {
				return // terminal — no more transitions coming
			}
		}
	}
}
