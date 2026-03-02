package gatekeeper

import (
	"context"
	"fmt"
	"math"

	log "jmdn/logging"

	"jmdn/config/settings"

	"github.com/JupiterMetaLabs/ion"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// Limiter is the interface consumed by middleware.
// Implementations must be safe for concurrent use.
type Limiter interface {
	// Allow reports whether a request from ip to serviceName is permitted.
	Allow(ctx context.Context, serviceName string, ip string) bool

	// RetryAfterSeconds returns the recommended wait time (in seconds)
	// for a client that was just rate-limited on the given service+ip.
	// Returns 0 if unknown.
	RetryAfterSeconds(serviceName string, ip string) int
}

// RateLimiter implements two-layer rate limiting:
//
//	Layer 1 (Global per-IP):   aggregate cap across all services → DDoS defense
//	Layer 2 (Per-IP per-Svc):  per-service cap per IP → fairness / policy enforcement
//
// Both layers use LRU caches to keep memory bounded.
// Implements the Limiter interface.
type RateLimiter struct {
	config *settings.SecurityConfig

	// Layer 1: global per-IP limiter ("1.2.3.4" → aggregate limiter)
	perIP *lru.Cache[string, *rate.Limiter]

	// Layer 2: per-service per-IP limiter ("eth_rpc:1.2.3.4" → service limiter)
	perIPSvc *lru.Cache[string, *rate.Limiter]

	logger *ion.Ion
}

// Compile-time assertion: *RateLimiter implements Limiter.
var _ Limiter = (*RateLimiter)(nil)

// NewRateLimiter creates a new two-layer RateLimiter.
// cacheSize defaults to 1,000 active IPs if 0 is passed.
func NewRateLimiter(cfg *settings.SecurityConfig, cacheSize int) (*RateLimiter, error) {
	if cacheSize <= 0 {
		cacheSize = 1000
	}

	perIP, err := lru.New[string, *rate.Limiter](cacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create global rate limit cache: %w", err)
	}

	perIPSvc, err := lru.New[string, *rate.Limiter](cacheSize * 4) // ~4 services per IP on average
	if err != nil {
		return nil, fmt.Errorf("failed to create per-service rate limit cache: %w", err)
	}

	return &RateLimiter{
		config:   cfg,
		perIP:    perIP,
		perIPSvc: perIPSvc,
		logger:   logger(log.RateLimit),
	}, nil
}

// Allow checks if the request from the given IP for the given service is allowed.
// Returns true if allowed, false if rate limited.
//
// Layer 1 (global per-IP) runs first; if it denies, the request is rejected immediately.
// Layer 2 (per-service per-IP) runs second to enforce per-service limits.
func (rl *RateLimiter) Allow(ctx context.Context, serviceName string, ip string) bool {
	// --- Layer 1: Global per-IP cap ---
	if rl.config.GlobalRateLimit > 0 {
		limiter := rl.getOrCreate(rl.perIP, ip, rl.config.GlobalRateLimit, rl.config.GlobalBurst)
		if !limiter.Allow() {
			if rl.logger != nil {
				rl.logger.Warn(ctx, "Global rate limit exceeded",
					ion.String("ip", ip), ion.String("attempted_service", serviceName))
			}
			return false
		}
	}

	// --- Layer 2: Per-service per-IP ---
	policy, ok := rl.config.Services[serviceName]
	if !ok {
		if rl.logger != nil {
			rl.logger.Warn(ctx, "RateLimit: Service not found in config, allowing request", ion.String("service", serviceName))
		}
		return true
	}

	// Skip rate limiting if disabled for this service (e.g. BFT consensus)
	if policy.RateLimit <= 0 {
		return true
	}

	// Use string concat — avoids fmt.Sprintf allocation on hot path
	key := serviceName + ":" + ip

	limiter := rl.getOrCreate(rl.perIPSvc, key, policy.RateLimit, policy.Burst)
	return limiter.Allow()
}

// RetryAfterSeconds calculates a recommended wait time based on the service's rate limit.
// Uses ceiling(1/RPS) so clients know the minimum time before a token is available.
func (rl *RateLimiter) RetryAfterSeconds(serviceName string, ip string) int {
	policy, ok := rl.config.Services[serviceName]
	if !ok || policy.RateLimit <= 0 {
		return 1 // Sensible default
	}
	// Ceiling of 1/RPS → minimum seconds until a new token is available
	seconds := int(math.Ceil(1.0 / policy.RateLimit))
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// getOrCreate retrieves an existing limiter from the cache or creates a new one.
func (rl *RateLimiter) getOrCreate(cache *lru.Cache[string, *rate.Limiter], key string, rps float64, burst int) *rate.Limiter {
	limiter, exists := cache.Get(key)
	if exists {
		return limiter
	}

	if burst <= 0 {
		burst = int(rps) // Default burst to 1x RPS if not set
	}

	limiter = rate.NewLimiter(rate.Limit(rps), burst)
	cache.Add(key, limiter)
	return limiter
}
