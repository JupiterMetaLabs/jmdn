package gatekeeper

import (
	"context"
	"fmt"

	log "gossipnode/logging"

	"gossipnode/config/settings"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// GrpcMiddleware provides gRPC middleware for security
type GrpcMiddleware struct {
	config      *settings.SecurityConfig
	rateLimiter Limiter
	ipExtractor *IPExtractor
	logger      *ion.Ion
}

// NewGrpcMiddleware creates a new interceptor.
// Accepts the Limiter interface so callers can inject mocks for testing.
func NewGrpcMiddleware(cfg *settings.SecurityConfig, rl Limiter) *GrpcMiddleware {
	return &GrpcMiddleware{
		config:      cfg,
		rateLimiter: rl,
		ipExtractor: NewIPExtractor(cfg), // Pre-allocate — no per-request alloc
		logger:      logger(log.AuthGRPC),
	}
}

// UnaryInterceptor returns a UnaryServerInterceptor that enforces security policy
func (i *GrpcMiddleware) UnaryInterceptor(serviceName string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := i.enforcePolicy(ctx, serviceName); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a StreamServerInterceptor that enforces security policy
func (i *GrpcMiddleware) StreamInterceptor(serviceName string) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := i.enforcePolicy(ss.Context(), serviceName); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// enforcePolicy checks Rate Limit, mTLS, and Token Auth
func (i *GrpcMiddleware) enforcePolicy(ctx context.Context, serviceName string) error {
	if i.config != nil && !i.config.Enabled {
		return nil // Globally disabled, bypass security
	}

	policy, ok := i.config.Services[serviceName]
	if !ok {
		i.logger.Error(ctx, "Security: Unknown service policy", fmt.Errorf("missing policy for service: %s", serviceName))
		return status.Error(codes.Internal, "Internal Server Error: Security Policy Missing")
	}

	// Determine Client IP (True IP via pre-allocated extractor)
	clientIP := i.ipExtractor.GetClientIP(ctx)

	// Get Peer Info for mTLS verification
	peerInfo, _ := peer.FromContext(ctx)

	// Rate Limiting
	if i.rateLimiter != nil && !i.rateLimiter.Allow(ctx, serviceName, clientIP) {
		i.logger.Warn(ctx, "Rate Limit Exceeded", ion.String("service", serviceName), ion.String("ip", clientIP))
		return status.Error(codes.ResourceExhausted, "Rate limit exceeded")
	}

	// Authentication
	return i.authenticate(ctx, policy, peerInfo)
}

// authenticate verifies credentials based on AuthType
func (i *GrpcMiddleware) authenticate(ctx context.Context, policy settings.Policy, peerInfo *peer.Peer) error {
	// Case A: No Auth Required
	if policy.AuthType == settings.AuthTypeNone {
		return nil
	}

	// Case B: mTLS check
	isMTLS := false
	if peerInfo != nil && peerInfo.AuthInfo != nil {
		if tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.VerifiedChains) > 0 {
				isMTLS = true
			}
		}
	}

	if policy.AuthType == settings.AuthTypeMTLS {
		if !isMTLS {
			return status.Error(codes.Unauthenticated, "mTLS Certificate Required")
		}
		return nil
	}

	// Case C: Hybrid — mTLS satisfied, skip token check
	if policy.AuthType == settings.AuthTypeHybrid && isMTLS {
		return nil
	}

	// Case D: Token auth (Token or Hybrid fallback)
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "No metadata found")
	}
	tokens := md["authorization"]
	if len(tokens) == 0 {
		return status.Error(codes.Unauthenticated, "Authorization token required")
	}

	// Delegate to shared token validation
	if _, err := ValidateAuthHeader(tokens[0], policy, i.config); err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}

	return nil
}
