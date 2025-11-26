package rpc

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"gossipnode/gETH/Facade/Service/Logger"
	AppContext "gossipnode/config/Context"
	"github.com/gin-gonic/gin"
)

const(
	HTTPServerAppContext = "geth.http.server"
)

type HTTPServer struct {
	h *Handlers
}

func NewHTTPServer(h *Handlers) *HTTPServer {
	Logger.Once.Do(func() {
		if err := Logger.InitLogger(); err != nil {
			// Log error but don't panic - continue without logger
			fmt.Printf("Warning: failed to initialize logger: %v\n", err)
		}
	})
	return &HTTPServer{h: h}
}

func (s *HTTPServer) Serve(addr string) error {
	// Set GIN mode to release for production
	gin.SetMode(gin.ReleaseMode)

	// Create GIN router
	router := gin.New()

	// Add middleware
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(withCORS())

	// Add JSON-RPC handler
	router.Any("/", s.handleJSONRPC)

	// Create HTTP server with GIN router
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *HTTPServer) handleJSONRPC(c *gin.Context) {
	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
		write(c, RespErr(nil, -32700, "Parse error"))
		return
	}
	longCTX, _ := AppContext.GetAppContext(HTTPServerAppContext).NewChildContext()
	resp, _ := s.h.Handle(longCTX, req)
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
