package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"

	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/dualdb"
	"gossipnode/config/settings"
	"gossipnode/logging"
	"gossipnode/pkg/gatekeeper"
)

type HTTPServer struct {
	h       *Handlers
	dualDB  *dualdb.DualDB
	cassata *cassata.Cassata // optional: Postgres projection reads when Thebe is enabled
	logger  *ion.Ion        // Add logger
}

func NewHTTPServer(h *Handlers) *HTTPServer {
	// Initialize logger
	l, _ := logging.NewAsyncLogger().Get().NamedLogger("JSONRPC", "")

	return &HTTPServer{h: h, logger: l.NamedLogger}
}

func (s *HTTPServer) WithDualDB(d *dualdb.DualDB) *HTTPServer {
	s.dualDB = d
	return s
}

// WithCassata wires read-only Thebe projection APIs (see registerThebeReadRoutes).
func (s *HTTPServer) WithCassata(c *cassata.Cassata) *HTTPServer {
	s.cassata = c
	return s
}

func (s *HTTPServer) WithDualDB(d *dualdb.DualDB) *HTTPServer {
	s.dualDB = d
	return s
}

// WithCassata wires read-only Thebe projection APIs (see registerThebeReadRoutes).
func (s *HTTPServer) WithCassata(c *cassata.Cassata) *HTTPServer {
	s.cassata = c
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
	router.GET("/debug/dualdb/report", s.DualDBReport)
	s.registerThebeReadRoutes(router)

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

func (s *HTTPServer) DualDBReport(c *gin.Context) {
	if s.dualDB == nil {
		c.String(http.StatusServiceUnavailable, "dualdb not enabled")
		return
	}

	c.Header("Content-Type", "application/json")
	if err := json.NewEncoder(c.Writer).Encode(s.dualDB.Report()); err != nil {
		c.String(http.StatusInternalServerError, "failed to encode dualdb report")
		return
	}
}

func (s *HTTPServer) handleJSONRPC(c *gin.Context) {
	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
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
