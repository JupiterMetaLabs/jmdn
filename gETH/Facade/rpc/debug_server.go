package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// loopbackDebugAddr forces the debug server to bind to loopback only. The debug
// surface exposes internal Thebe/DualDB read routes with permissive CORS and no
// auth, so it must never listen on a public/all-interfaces address. If the host
// portion is empty or a wildcard (0.0.0.0 / ::), it is rewritten to 127.0.0.1
// while preserving the port.
//
// TODO: when a config-driven allowlist/auth exists, allow an explicit opt-in to
// a non-loopback bind for operators fronting this behind a trusted gateway.
func loopbackDebugAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// addr has no port (or is malformed): assume it is a bare port/host and
		// bind loopback with whatever was given as the port.
		return "127.0.0.1:" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return net.JoinHostPort("127.0.0.1", port)
	default:
		return net.JoinHostPort(host, port)
	}
}

// ServeDebugWithContext starts a dedicated debug server for Thebe/DualDB routes.
// Keep this on a separate bind/port from public RPC.
func (s *HTTPServer) ServeDebugWithContext(ctx context.Context, addr string) error {
	gin.SetMode(gin.ReleaseMode)

	// Force loopback: this debug surface has no auth and permissive CORS.
	addr = loopbackDebugAddr(addr)

	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(withCORS())
	s.registerThebeReadRoutes(router)

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
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
