package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/internal/syncmonitor"
	"gossipnode/logging"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/ion"
)

type HTTPServer struct {
	h           *Handlers
	logger      *ion.Ion // Add logger
	syncMonitor *syncmonitor.Monitor
}

func NewHTTPServer(h *Handlers) *HTTPServer {
	// Initialize logger
	l, _ := logging.NewAsyncLogger().Get().NamedLogger("JSONRPC", "")

	return &HTTPServer{h: h, logger: l.NamedLogger}
}

// WithSyncMonitor attaches a SyncMonitor so the server exposes /sync/* routes.
func (s *HTTPServer) WithSyncMonitor(m *syncmonitor.Monitor) *HTTPServer {
	s.syncMonitor = m
	return s
}

func (s *HTTPServer) Serve(addr string) error {
	return s.ServeWithContext(context.Background(), addr)
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

	// Initialize Security via gatekeeper helper
	secCfg := &settings.Get().Security
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	tlsEnabled, middleware, err := gatekeeper.ConfigureHTTPServer(srv, settings.ServiceEthRPC, secCfg, s.logger)
	if err != nil {
		return fmt.Errorf("failed to configure secure HTTP server: %w", err)
	}

	// Apply Gatekeeper Middleware
	router.Use(middleware.Middleware(settings.ServiceEthRPC))

	// Add JSON-RPC handler
	router.Any("/", s.handleJSONRPC)

	// Add sync status/reconcile endpoints if a monitor is wired in
	if s.syncMonitor != nil {
		RegisterSyncRoutes(router, s.syncMonitor)
	}

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

const maxBatchSize = 100

func (s *HTTPServer) handleJSONRPC(c *gin.Context) {
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
		if len(reqs) > maxBatchSize {
			write(c, RespErr(nil, -32600, fmt.Sprintf("batch too large: max %d requests", maxBatchSize)))
			return
		}
		resps := make([]Response, len(reqs))
		var wg sync.WaitGroup
		for i, req := range reqs {
			wg.Add(1)
			go func(i int, req Request) {
				defer wg.Done()
				resps[i], _ = s.h.Handle(c.Request.Context(), req)
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
	resp, _ := s.h.Handle(c.Request.Context(), req)
	write(c, resp)
}

func write(c *gin.Context, resp Response) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, resp)
}

func withCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "content-type")
		if c.Request.Method == http.MethodOptions {
			c.Status(204)
			c.Abort()
			return
		}
		c.Next()
	}
}
