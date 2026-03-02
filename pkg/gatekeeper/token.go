package gatekeeper

import (
	"crypto/subtle"

	"jmdn/config/settings"

	"github.com/golang-jwt/jwt/v5"
)

// AuthResult contains the outcome of a successful authentication.
// Claims is non-nil only when the token was a valid JWT.
type AuthResult struct {
	Claims jwt.MapClaims
}

// ValidateAuthHeader checks a raw "Authorization" header value against a service policy.
// It supports both JWT validation (if JWTSecret is configured) and static token matching.
// This is the single source of truth for token-based auth — used by both gRPC and Gin middleware.
//
// On success, returns an AuthResult containing JWT claims (if applicable).
func ValidateAuthHeader(authHeader string, policy settings.Policy, cfg *settings.SecurityConfig) (*AuthResult, error) {
	if authHeader == "" {
		return nil, errAuthRequired
	}

	// Parse "Bearer <token>"
	_, token, ok := parseBearer(authHeader)
	if !ok {
		return nil, errInvalidFormat
	}

	// 1. Try JWT validation (if a global JWT secret is configured)
	if cfg.JWTSecret != "" {
		claims, err := validateJWT(token, cfg.JWTSecret)
		if err == nil {
			return &AuthResult{Claims: claims}, nil
		}
		// JWT failed — fall through to static token check
	}

	// 2. Static token validation (constant-time to prevent timing attacks)
	// Uses the startup-resolved cache — no os.Getenv on hot path
	validToken := cfg.GetResolvedToken(policy.TokenEnv)
	if validToken != "" && constantTimeEqual(token, validToken) {
		return &AuthResult{}, nil
	}

	return nil, errInvalidToken
}

// constantTimeEqual compares two strings in constant time.
// This prevents timing side-channel attacks on token comparison.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
