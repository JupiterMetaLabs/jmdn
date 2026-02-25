package gatekeeper

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"gossipnode/config/settings"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewSecureGRPCServer creates a grpc.Server with TLS, rate limiting, and auth interceptors
// configured from the security policy for the given service.
//
// This replaces the ~15-line boilerplate that was duplicated across 6 servers.
// Callers can pass additional grpc.ServerOption (e.g. max message sizes) via extraOpts.
// Set includeStreamInterceptor to true if the server uses gRPC streaming RPCs.
//
// Returns: the grpc.Server and the loaded *tls.Config (nil if TLS is disabled).
func NewSecureGRPCServer(
	serviceName string,
	secCfg *settings.SecurityConfig,
	logger *ion.Ion,
	includeStreamInterceptor bool,
	extraOpts ...grpc.ServerOption,
) (*grpc.Server, *tls.Config, error) {
	// 1. TLS
	tlsLoader := NewTLSLoader(secCfg, logger)
	serverTLS, err := tlsLoader.LoadServerTLS(serviceName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS for %s: %w", serviceName, err)
	}

	var opts []grpc.ServerOption
	if serverTLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(serverTLS)))
	}

	// 2. Rate Limiter + Middleware
	rl, err := NewRateLimiter(secCfg, secCfg.IPCacheSize)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create rate limiter for %s: %w", serviceName, err)
	}
	obs := NewObsLimiter(context.Background(), rl, 30*time.Second) // ROLLOUT-OBS: remove obs line + change obs→rl below
	gk := NewGrpcMiddleware(secCfg, obs)

	// 3. Interceptors
	opts = append(opts, grpc.UnaryInterceptor(gk.UnaryInterceptor(serviceName)))
	if includeStreamInterceptor {
		opts = append(opts, grpc.StreamInterceptor(gk.StreamInterceptor(serviceName)))
	}

	// 4. Caller-supplied options (message sizes, etc.)
	opts = append(opts, extraOpts...)

	return grpc.NewServer(opts...), serverTLS, nil
}

// ConfigureHTTPServer applies the TLS configuration from the security policy
// to an existing *http.Server. Also initializes rate limiting and returns
// the Gin middleware for the caller to apply to their router.
//
// Returns (tlsEnabled, ginMiddleware, error).
// If TLS is enabled, the caller should use srv.ListenAndServeTLS("", "")
// instead of srv.ListenAndServe().
func ConfigureHTTPServer(
	srv *http.Server,
	serviceName string,
	secCfg *settings.SecurityConfig,
	logger *ion.Ion,
) (tlsEnabled bool, middleware *GinMiddleware, err error) {
	// 1. Rate Limiter
	rl, err := NewRateLimiter(secCfg, secCfg.IPCacheSize)
	if err != nil {
		return false, nil, fmt.Errorf("failed to init rate limiter for %s: %w", serviceName, err)
	}
	obs := NewObsLimiter(context.Background(), rl, 30*time.Second) // ROLLOUT-OBS: remove obs line + change obs→rl below

	// 2. Gin Middleware
	middleware = NewGinMiddleware(secCfg, obs, logger)

	// 3. TLS
	tlsLoader := NewTLSLoader(secCfg, logger)
	tlsConfig, err := tlsLoader.LoadServerTLS(serviceName)
	if err != nil {
		return false, nil, fmt.Errorf("failed to load TLS for %s: %w", serviceName, err)
	}

	if tlsConfig != nil {
		srv.TLSConfig = tlsConfig
		return true, middleware, nil
	}

	return false, middleware, nil
}

// ServeHTTP starts an http.Server using TLS or plain depending on the configured policy.
// This is a convenience wrapper; callers who need more control should use ConfigureHTTPServer.
func ServeHTTP(srv *http.Server, tlsEnabled bool) error {
	if tlsEnabled {
		// Certs are already in srv.TLSConfig — empty strings tell Go to use them
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

// ConfigureNetHTTPServer is the net/http equivalent of ConfigureHTTPServer.
// Use this when the HTTP server uses http.ServeMux (not Gin) — e.g. gorilla/websocket servers.
// The caller must wire the returned middleware:
//
//	srv.Handler = middleware.Wrap(serviceName, existingMux)
//
// This ensures rate limiting fires on every HTTP request (including WS upgrade requests)
// before the handler runs.
func ConfigureNetHTTPServer(
	srv *http.Server,
	serviceName string,
	secCfg *settings.SecurityConfig,
	l *ion.Ion,
) (tlsEnabled bool, middleware *NetHTTPMiddleware, err error) {
	// 1. Rate Limiter
	rl, err := NewRateLimiter(secCfg, secCfg.IPCacheSize)
	if err != nil {
		return false, nil, fmt.Errorf("failed to init rate limiter for %s: %w", serviceName, err)
	}
	obs := NewObsLimiter(context.Background(), rl, 30*time.Second) // ROLLOUT-OBS: remove obs line + change obs→rl below

	// 2. net/http Middleware
	middleware = NewNetHTTPMiddleware(secCfg, obs, l)

	// 3. TLS
	tlsLoader := NewTLSLoader(secCfg, l)
	tlsConfig, err := tlsLoader.LoadServerTLS(serviceName)
	if err != nil {
		return false, nil, fmt.Errorf("failed to load TLS for %s: %w", serviceName, err)
	}

	if tlsConfig != nil {
		srv.TLSConfig = tlsConfig
		return true, middleware, nil
	}

	return false, middleware, nil
}
