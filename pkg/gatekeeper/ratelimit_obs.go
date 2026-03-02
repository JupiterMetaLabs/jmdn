package gatekeeper

// =============================================================================
// ROLLOUT-OBS: This file is a TEMPORARY observability wrapper for rate limiting.
//
// It exists to give visibility into rate limiting behaviour during the initial
// production rollout on JMDN nodes. It is NOT intended to stay permanently.
//
// TO REMOVE COMPLETELY:
//   1. Delete this file (ratelimit_obs.go).
//   2. Find the 4 lines marked "// ROLLOUT-OBS:" in Block/Server.go,
//      explorer/api.go, and pkg/gatekeeper/server.go (2 sites there).
//   3. At each site: remove the `obs :=` line; change `obs` back to `rl`.
//      That's it — no other code was touched.
// =============================================================================

import (
	"context"
	"time"

	log "jmdn/logging"

	"github.com/JupiterMetaLabs/ion"
)

// ObsLimiter is a transparent decorator around *RateLimiter.
// It implements the Limiter interface and adds Info-level logs on every
// rate limit check and periodic LRU cache stats, without touching the
// core rate limiting logic.
type ObsLimiter struct {
	inner  *RateLimiter
	logger *ion.Ion
}

// Compile-time assertion: *ObsLimiter implements Limiter.
var _ Limiter = (*ObsLimiter)(nil)

// NewObsLimiter wraps rl with observability logging and starts a background
// goroutine that logs LRU cache stats every interval. The goroutine stops
// when ctx is cancelled (pass context.Background() for process-lifetime servers).
func NewObsLimiter(ctx context.Context, rl *RateLimiter, interval time.Duration) *ObsLimiter {
	obs := &ObsLimiter{
		inner:  rl,
		logger: logger(log.RateLimit),
	}
	obs.startCacheStatsLogger(ctx, interval)
	return obs
}

// Allow delegates to the inner RateLimiter and logs the outcome at Info level.
// This fires on EVERY request — allowed or denied — to provide full rollout
// visibility without requiring debug log level (which would flood OTel).
func (o *ObsLimiter) Allow(ctx context.Context, serviceName string, ip string) bool {
	allowed := o.inner.Allow(ctx, serviceName, ip)

	if o.logger != nil {
		o.logger.Info(ctx, "Rate limit check", // ROLLOUT-OBS
			ion.String("ip", ip),
			ion.String("service", serviceName),
			ion.Bool("allowed", allowed))
	}

	return allowed
}

// RetryAfterSeconds delegates to the inner RateLimiter unchanged.
func (o *ObsLimiter) RetryAfterSeconds(serviceName string, ip string) int {
	return o.inner.RetryAfterSeconds(serviceName, ip)
}

// startCacheStatsLogger runs a goroutine that logs LRU occupancy every interval.
// Saturation approaching 100% means the LRU is evicting active IPs — a signal
// to increase ip_cache_size in Ansible config.
func (o *ObsLimiter) startCacheStatsLogger(ctx context.Context, interval time.Duration) {
	if o.logger == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l1Len := o.inner.perIP.Len()
				l2Len := o.inner.perIPSvc.Len()

				o.logger.Info(context.Background(), "Rate limit cache stats", // ROLLOUT-OBS
					ion.Int("global_cache_len", l1Len),
					ion.Int("per_svc_cache_len", l2Len))
			}
		}
	}()
}
