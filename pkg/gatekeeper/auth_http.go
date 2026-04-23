package gatekeeper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"gossipnode/config/settings"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// NetHTTPMiddleware provides security middleware for net/http-based servers
// (e.g. http.ServeMux, gorilla/mux). It is the net/http equivalent of GinMiddleware
// and shares the same Limiter, IPExtractor, and policy-enforcement logic.
//
// Primary use-case: wrapping the ETH WebSocket server mux, where rate limiting fires
// on the HTTP upgrade request — before gorilla/websocket upgrades the connection.
// This correctly limits the rate of new WS connection attempts per IP.
type NetHTTPMiddleware struct {
	config      *settings.SecurityConfig
	rateLimiter Limiter
	ipExtractor *IPExtractor
	logger      *ion.Ion
}

// NewNetHTTPMiddleware creates a new NetHTTPMiddleware.
// Accepts the Limiter interface so callers can inject mocks for testing.
// Accepts optional logger; falls back to default if nil.
func NewNetHTTPMiddleware(cfg *settings.SecurityConfig, rl Limiter, l *ion.Ion) *NetHTTPMiddleware {
	if l == nil {
		l = logger(log.AuthHTTP)
	}
	return &NetHTTPMiddleware{
		config:      cfg,
		rateLimiter: rl,
		ipExtractor: NewIPExtractor(cfg),
		logger:      l,
	}
}

// Wrap returns an http.Handler that enforces security policy for the given service,
// then delegates to next if the request is permitted.
func (m *NetHTTPMiddleware) Wrap(serviceName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := m.enforcePolicy(r, serviceName); err != nil {
			if err == errRateLimited {
				retryAfter := 1
				if m.rateLimiter != nil {
					retryAfter = m.rateLimiter.RetryAfterSeconds(serviceName, m.ipExtractor.GetClientIPFromRequest(r))
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				m.writeJSON(w, http.StatusTooManyRequests, err.Error())
				return
			}
			m.writeJSON(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// enforcePolicy checks rate limiting and authentication for a plain HTTP request.
func (m *NetHTTPMiddleware) enforcePolicy(r *http.Request, serviceName string) error {
	if m.config != nil && !m.config.Enabled {
		return nil // Globally disabled
	}

	policy, ok := m.config.Services[serviceName]
	if !ok {
		if m.logger != nil {
			m.logger.Error(r.Context(), "Security: Unknown service policy",
				fmt.Errorf("missing policy for service: %s", serviceName))
		}
		return errUnknownService
	}

	clientIP := m.ipExtractor.GetClientIPFromRequest(r)

	// Rate Limiting
	if m.rateLimiter != nil && !m.rateLimiter.Allow(r.Context(), serviceName, clientIP) {
		if m.logger != nil {
			m.logger.Warn(r.Context(), "Rate Limit Exceeded",
				ion.String("service", serviceName), ion.String("ip", clientIP))
		}
		return errRateLimited
	}

	// Authentication
	return m.authenticate(r, policy)
}

// authenticate validates credentials based on the policy's AuthType.
func (m *NetHTTPMiddleware) authenticate(r *http.Request, policy settings.Policy) error {
	if policy.AuthType == settings.AuthTypeNone {
		return nil
	}

	if policy.AuthType == settings.AuthTypeToken || policy.AuthType == settings.AuthTypeHybrid {
		authHeader := r.Header.Get("Authorization")
		_, err := ValidateAuthHeader(authHeader, policy, m.config)
		return err
	}

	return nil
}

// writeJSON writes a JSON error response. Kept minimal — no Gin dependency.
func (m *NetHTTPMiddleware) writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
