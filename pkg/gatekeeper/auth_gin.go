package gatekeeper

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"gossipnode/config/settings"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"
)

// errRateLimited is a sentinel indicating the request was denied by the rate limiter.
// Middleware uses this to send 429 + Retry-After instead of a generic 403.
var errRateLimited = errors.New("rate limit exceeded")

// GinMiddleware provides security middleware for Gin servers
type GinMiddleware struct {
	config      *settings.SecurityConfig
	rateLimiter Limiter
	ipExtractor *IPExtractor
	logger      *ion.Ion
}

// NewGinMiddleware creates a new middleware instance.
// Accepts the Limiter interface so callers can inject mocks for testing.
// Accepts optional logger for DI (testability), falls back to default if nil.
func NewGinMiddleware(cfg *settings.SecurityConfig, rl Limiter, l *ion.Ion) *GinMiddleware {
	if l == nil {
		l = logger(log.AuthGin)
	}
	return &GinMiddleware{
		config:      cfg,
		rateLimiter: rl,
		ipExtractor: NewIPExtractor(cfg), // Pre-allocate — no per-request alloc
		logger:      l,
	}
}

// Middleware returns a Gin Handler that enforces security policy for a service.
func (m *GinMiddleware) Middleware(serviceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := m.enforcePolicy(c, serviceName)
		if err == nil {
			c.Next()
			return
		}

		// Rate limited → 429 + Retry-After header (RFC 6585)
		if errors.Is(err, errRateLimited) {
			retryAfter := 1
			if m.rateLimiter != nil {
				retryAfter = m.rateLimiter.RetryAfterSeconds(serviceName, m.ipExtractor.GetClientIPFromRequest(c.Request))
			}
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}

		// All other auth errors → 403 Forbidden
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": err.Error()})
	}
}

// enforcePolicy checks Rate Limit and Authentication.
// Returns errRateLimited for rate limit denials, other errors for auth failures.
func (m *GinMiddleware) enforcePolicy(c *gin.Context, serviceName string) error {
	if m.config != nil && !m.config.Enabled {
		return nil // Globally disabled, bypass security
	}

	policy, ok := m.config.Services[serviceName]
	if !ok {
		m.logger.Error(c.Request.Context(), "Security: Unknown service policy", fmt.Errorf("missing policy for service: %s", serviceName))
		return fmt.Errorf("internal security error")
	}

	// Determine Client IP via pre-allocated extractor
	clientIP := m.ipExtractor.GetClientIPFromRequest(c.Request)

	// Rate Limiting
	if m.rateLimiter != nil && !m.rateLimiter.Allow(c.Request.Context(), serviceName, clientIP) {
		m.logger.Warn(c.Request.Context(), "Rate Limit Exceeded", ion.String("service", serviceName), ion.String("ip", clientIP))
		return errRateLimited
	}

	// Authentication
	return m.authenticate(c, policy)
}

// authenticate verifies credentials based on AuthType
func (m *GinMiddleware) authenticate(c *gin.Context, policy settings.Policy) error {
	// Case A: No Auth Required
	if policy.AuthType == settings.AuthTypeNone {
		return nil
	}

	// Case B: Token Auth (HTTP Header) — delegate to shared validator
	if policy.AuthType == settings.AuthTypeToken || policy.AuthType == settings.AuthTypeHybrid {
		authHeader := c.GetHeader("Authorization")
		result, err := ValidateAuthHeader(authHeader, policy, m.config)
		if err != nil {
			return err
		}

		// Inject JWT claims if present (single-parse: claims come from ValidateAuthHeader)
		if result != nil && result.Claims != nil {
			c.Set("token_claims", result.Claims)
		}
		return nil
	}

	return nil
}
