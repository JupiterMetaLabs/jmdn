package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gossipnode/internal/saferun"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"

	"gossipnode/DB_OPs/cassata"
	"gossipnode/config/settings"
	"gossipnode/internal/syncmonitor"
	"gossipnode/logging"
	"gossipnode/pkg/gatekeeper"
)

type HTTPServer struct {
	h           *Handlers
	cassata     *cassata.Cassata // optional: Postgres projection reads when Thebe is enabled
	logger      *ion.Ion         // Add logger
	syncMonitor *syncmonitor.Monitor
	gate        *gatekeeper.GinMiddleware // set by ServeWithContext; charges batch members
}

func NewHTTPServer(h *Handlers) *HTTPServer {
	// Initialize logger
	l, err := logging.NewAsyncLogger().Get().NamedLogger("JSONRPC", "")
	if err != nil || l == nil || l.NamedLogger == nil {
		return &HTTPServer{h: h}
	}

	return &HTTPServer{h: h, logger: l.NamedLogger}
}

// WithCassata wires read-only Thebe projection APIs (see registerThebeReadRoutes).
func (s *HTTPServer) WithCassata(c *cassata.Cassata) *HTTPServer {
	s.cassata = c
	return s
}

// WithSyncMonitor attaches a SyncMonitor so the server exposes /sync/* routes.
func (s *HTTPServer) WithSyncMonitor(m *syncmonitor.Monitor) *HTTPServer {
	s.syncMonitor = m
	return s
}

func (s *HTTPServer) ServeWithContext(ctx context.Context, addr string) error {
	// Set GIN mode to release for production
	gin.SetMode(gin.ReleaseMode)

	// Create GIN router
	router := gin.New()

	// Add middleware
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(withCORS())

	// Create HTTP server with GIN router
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB (JMDN-V3-013)
	}
	secCfg := &settings.Get().Security
	tlsEnabled, middleware, err := gatekeeper.ConfigureHTTPServer(srv, settings.ServiceEthRPC, secCfg, s.logger)
	if err != nil {
		return fmt.Errorf("failed to configure secure HTTP server: %w", err)
	}

	// Apply Gatekeeper Middleware
	router.Use(middleware.Middleware(settings.ServiceEthRPC))
	s.gate = middleware

	// Register routes after middleware so protection applies consistently.
	router.Any("/", s.handleJSONRPC)

	// Add sync status/reconcile endpoints if a monitor is wired in
	if s.syncMonitor != nil {
		RegisterSyncRoutes(router, s.syncMonitor)
	}
	RegisterConsensusAdminRoutes(router)

	errCh := make(chan error, 1)
	go func() {
		errCh <- gatekeeper.ServeHTTP(srv, tlsEnabled)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// maxBatchSize and maxBatchConcurrency are the compiled defaults for
// rpc.batch.*. maxBatchSize is what shipped; upstream geth's own default is
// 1000, so this is already the conservative end. maxBatchConcurrency is new —
// before it, the batch size WAS the concurrency.
const (
	maxBatchSize        = 100
	maxBatchConcurrency = 8
	// batchDeadline bounds a whole batch. Below the 30s WriteTimeout above, so
	// the response is still deliverable when the last element finishes.
	batchDeadline = 25 * time.Second
)

// batchLimits returns the batch bounds in force. Config wins; a zero or
// negative value falls back to the compiled default, so this fails closed
// rather than opening the batch path on a malformed config.
func batchLimits() (size, concurrency int) {
	size, concurrency = maxBatchSize, maxBatchConcurrency
	// settings.Get() panics before Load(), by design. The server is built after
	// Load, but this keeps the limits usable from tests and tools that never
	// load a config.
	if !settings.IsLoaded() {
		return size, concurrency
	}
	cfg := settings.Get()
	if cfg.RPC.Batch.MaxSize > 0 {
		size = cfg.RPC.Batch.MaxSize
	}
	if cfg.RPC.Batch.MaxConcurrency > 0 {
		concurrency = cfg.RPC.Batch.MaxConcurrency
	}
	return size, concurrency
}

// maxRequestBodyBytes bounds a single JSON-RPC request body (JMDN-V3-013).
// 10 MiB comfortably covers large batches / contract-deploy payloads while
// bounding per-request memory use.
const maxRequestBodyBytes = 10 << 20

func (s *HTTPServer) handleJSONRPC(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	body, err := c.GetRawData()
	if err != nil || len(body) == 0 {
		write(c, RespErr(nil, -32700, "Parse error"))
		return
	}

	// Detect batch vs single by inspecting first non-whitespace byte
	isBatch := false
	for _, b := range body {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		isBatch = b == '['
		break
	}

	if isBatch {
		var reqs []Request
		if err := json.Unmarshal(body, &reqs); err != nil {
			write(c, RespErr(nil, -32700, "Parse error"))
			return
		}
		if len(reqs) == 0 {
			write(c, RespErr(nil, -32600, "Invalid Request: empty batch"))
			return
		}
		batchLimit, batchConcurrency := batchLimits()
		if len(reqs) > batchLimit {
			write(c, RespErr(nil, -32600, fmt.Sprintf("batch too large: max %d requests", batchLimit)))
			return
		}
		// The middleware charged one request for this HTTP call; charge the
		// rest of the batch so batching does not multiply the client's rate.
		if !s.gate.AllowAdditional(c, settings.ServiceEthRPC, len(reqs)-1) {
			c.Header("Retry-After", strconv.Itoa(s.gate.RetryAfter(c, settings.ServiceEthRPC)))
			c.JSON(http.StatusTooManyRequests,
				RespErr(nil, -32005, fmt.Sprintf("rate limit exceeded: batch of %d requests", len(reqs))))
			return
		}
		resps := make([]Response, len(reqs))
		// Bounded fan-out. Every element used to get its own goroutine, so the
		// batch size was also the concurrency: one HTTP request could put
		// batchLimit CPU-heavy calls (eth_getLogs, eth_call) on the scheduler
		// at once, multiplying any single-request cost by up to that factor.
		// The semaphore keeps batching useful for cheap calls without letting
		// one request own the machine. Ordering of resps is unaffected — each
		// element still writes its own slot.
		//
		// BOUNDED IN TIME AS WELL AS WIDTH (review). Limiting the width alone
		// trades concurrency for wall time: batchLimit elements at
		// batchConcurrency a time is ceil(batchLimit/batchConcurrency) waves,
		// and each eth_getLogs carries its own 10s deadline (DB_OPs.GetLogs)
		// that is NOT tied to this connection. Enough slow elements and the
		// batch outruns the server's WriteTimeout: the node does every element's
		// work and the client, already disconnected, gets nothing and retries.
		// Before the semaphore the whole batch ran at once and finished in about
		// one element's time, so this was width-bounded and time-unbounded only
		// after that change.
		//
		// So: one deadline for the batch, below WriteTimeout, derived from the
		// request context so a client disconnect cancels it too. Elements
		// inherit it, and a slot is taken only while that context is alive —
		// nothing new starts once the answer can no longer be delivered.
		batchCtx, cancelBatch := context.WithTimeout(
			withAuthHeader(c.Request.Context(), c.GetHeader("Authorization")),
			batchDeadline,
		)
		defer cancelBatch()

		sem := make(chan struct{}, batchConcurrency)
		var wg sync.WaitGroup
	dispatch:
		for i, req := range reqs {
			select {
			case sem <- struct{}{}:
			case <-batchCtx.Done():
				// Deadline hit or client gone. Answer the elements that never
				// ran rather than leaving their slots zero-valued, and stop
				// dispatching: work whose answer cannot be delivered is work
				// this node should not do.
				for j := i; j < len(reqs); j++ {
					resps[j] = RespErr(reqs[j].ID, -32603, "batch deadline exceeded before this request ran")
				}
				break dispatch
			}
			wg.Add(1)
			go func(i int, req Request) {
				defer wg.Done()
				defer func() { <-sem }()
				// This goroutine is outside gin.Recovery: recover a handler
				// panic into a JSON-RPC internal error for this element only.
				defer saferun.RecoverInto("rpc.batch", func(any) {
					resps[i] = RespErr(req.ID, -32603, "Internal error")
				})
				resps[i], _ = s.h.Handle(batchCtx, req)
			}(i, req)
		}
		wg.Wait()
		c.Header("Content-Type", "application/json")
		c.JSON(http.StatusOK, resps)
		return
	}

	// Single request — identical to original behaviour
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		write(c, RespErr(nil, -32700, "Parse error"))
		return
	}
	resp := s.handleSafe(c, req)
	write(c, resp)
}

// handleSafe runs one request behind a panic boundary so a handler bug answers
// with a JSON-RPC internal error instead of a dropped connection (gin.Recovery
// would also catch it on this goroutine, but with a non-JSON-RPC 500).
func (s *HTTPServer) handleSafe(c *gin.Context, req Request) (resp Response) {
	defer saferun.RecoverInto("rpc.single", func(any) {
		resp = RespErr(req.ID, -32603, "Internal error")
	})
	resp, _ = s.h.Handle(withAuthHeader(c.Request.Context(), c.GetHeader("Authorization")), req)
	return resp
}

func write(c *gin.Context, resp Response) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, resp)
}

func withCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Previously reflected "*" for any origin. Restrict to loopback origins
		// (echoing the specific Origin so it composes with credentialed requests).
		// TODO: source an explicit allowlist from config once a field exists.
		if origin := c.Request.Header.Get("Origin"); isLoopbackOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "content-type")
		}
		if c.Request.Method == http.MethodOptions {
			c.Status(204)
			c.Abort()
			return
		}
		c.Next()
	}
}
